package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoxca/exorciser/internal/detect"
	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/exorciser/internal/union"
	"github.com/hoxca/exorciser/internal/xisfwrite"
	"github.com/spf13/cobra"
)

type unionOptions struct {
	output     string
	tolerance  float64
	arm        int
	thickness  int
	labelScale int
}

const (
	// backgroundName is the file recording the chosen sky window.
	backgroundName = "background.csv"
	// backgroundSize is the side of the background window. It is odd so the
	// window has a centre pixel.
	backgroundSize = 65
	// backgroundOrder is the filter preference for the clean master that
	// supplies the background window.
	backgroundOrder = "LRH"
)

func newUnionCmd() *cobra.Command {
	opts := &unionOptions{}

	cmd := &cobra.Command{
		Use:   "union",
		Short: "Merge the filter catalogues and mark each filter's missing artefacts",
		Long: `Merge the artefact catalogues of every filter found under --output into one
union catalogue, then add the entries missing from each filter to that filter's
annotated image in yellow.

Filters are the single-letter directories L, R, G, B, S, H and O that contain
artefacts.csv. Their letters are concatenated in that order to name the union
directory: L, R, G and B produce <output>/LRGB.

The same ghost sits at a slightly different position in each filter, because
every master is stacked from its own registered frames. A union artefact
therefore averages the positions of its observations: rows within --tolerance
pixels of a cluster are treated as the same artefact, and the merged coordinate
is the rounded mean. SNR, amp and ghost scale are averaged too, and the entries
are renumbered by decreasing mean |SNR|. The result is written to
<output>/<filters>/artefacts.csv.

For every filter, annotate's output
<output>/<filter>/<filter>_stack_detected.xisf is read back, a yellow cross and
the union number are drawn at each union artefact absent from that filter's
catalogue, and the result is written to
<output>/<filters>/<filter>_stack_detected.xisf with XISF window name
detected_<filter>. The existing red annotations are preserved, so a yellow mark
is an artefact another filter saw but this one did not.

union also picks a background sky window from the clean master of the first
filter in L, R, H order. The 65x65 window lies inside the image and clear of
every union artefact by three ghost sigmas. Stars are masked and the remaining
pixels are sigma-clipped; a window is only used when at least 75% of its pixels
survive and each of its four quadrants keeps at least 25%, so the statistics
rest on a representative sample. The winner is the most uniform window, judged
by the spread of its quadrant means, with closeness to the frame's typical sky
level as a tie-breaker. This rejects nebulosity even when it matches the median
level. The centre and its background statistics are written to
<output>/<filters>/background.csv, and the window is outlined in yellow on
every <filter>_stack_detected.xisf.

Run scan and annotate for every filter before union. At least one of L, R or H
must have a clean master.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.validate(); err != nil {
				return err
			}
			return runUnion(cmd.Context(), opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&opts.output, "output", "o", "output",
		"root directory holding <filter>/artefacts.csv and receiving the union directory")
	flags.Float64Var(&opts.tolerance, "tolerance", 16,
		"maximum distance in pixels between two catalogued positions of the same artefact")
	flags.IntVar(&opts.arm, "arm", 12, "cross arm length in pixels")
	flags.IntVar(&opts.thickness, "thickness", 3, "cross line thickness in pixels")
	flags.IntVar(&opts.labelScale, "label-scale", 3,
		"union number scale in pixels; 0 hides numbers")
	return cmd
}

func (o *unionOptions) validate() error {
	if strings.TrimSpace(o.output) == "" {
		return errors.New("--output must not be empty")
	}
	if o.tolerance <= 0 || math.IsNaN(o.tolerance) || math.IsInf(o.tolerance, 0) {
		return fmt.Errorf("--tolerance must be a positive number, got %v", o.tolerance)
	}
	if o.arm < 0 {
		return fmt.Errorf("--arm must not be negative, got %d", o.arm)
	}
	if o.thickness < 1 {
		return fmt.Errorf("--thickness must be positive, got %d", o.thickness)
	}
	if o.labelScale < 0 {
		return fmt.Errorf("--label-scale must not be negative, got %d", o.labelScale)
	}
	return nil
}

func runUnion(ctx context.Context, opts *unionOptions) error {
	filters, err := readUnionFilters(opts.output)
	if err != nil {
		return err
	}
	names := make([]string, len(filters))
	for i, f := range filters {
		names[i] = f.Name
	}
	name := strings.Join(names, "")
	outDir := filepath.Join(opts.output, name)
	slog.Info("filter catalogues merged",
		"filters", name,
		"catalogues", len(filters))

	merged := union.Merge(filters, opts.tolerance)
	if len(merged) == 0 {
		return fmt.Errorf("the union catalogue is empty: every filter catalogue is empty")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	unionPath := filepath.Join(outDir, artefactsName)
	if err := writeUnionArtefacts(unionPath, merged); err != nil {
		return err
	}
	slog.Info("union catalogue written",
		"path", unionPath,
		"artefacts", len(merged))

	background, bgWidth, bgHeight, err := findBackground(opts.output, filters, merged)
	if err != nil {
		return err
	}
	backgroundPath := filepath.Join(outDir, backgroundName)
	if err := writeBackgroundFile(backgroundPath, background); err != nil {
		return err
	}
	slog.Info("background window written",
		"path", backgroundPath,
		"filter", background.Filter,
		"x", background.X,
		"y", background.Y,
		"size", background.Size,
		"mean", background.Mean,
		"noise", background.Noise,
		"snr", background.SNR,
		"pixels", background.Pixels)

	width, height := 0, 0
	for _, f := range filters {
		if err := ctx.Err(); err != nil {
			return err
		}
		missing := union.Missing(merged, f, opts.tolerance)

		source := filepath.Join(opts.output, f.Name, stackFileName(stackDetectedKind, f.Name))
		rgb, w, h, err := readRGBImage(source)
		if err != nil {
			return fmt.Errorf("read %s: %w", source, err)
		}
		if w != bgWidth || h != bgHeight {
			return fmt.Errorf("%s is %d×%d but the clean master is %d×%d; background positions need a common frame",
				source, w, h, bgWidth, bgHeight)
		}
		if width == 0 {
			width, height = w, h
		} else if w != width || h != height {
			return fmt.Errorf("%s is %d×%d but the first filter image is %d×%d; union positions need a common frame",
				source, w, h, width, height)
		}

		for _, m := range missing {
			drawCross(rgb, w, h, m.X, m.Y, opts.arm, opts.thickness, paintYellow)
		}
		for _, m := range missing {
			drawNumber(rgb, w, h, m.N, m.X, m.Y, opts.arm, opts.labelScale, paintYellow)
		}
		drawSquare(rgb, w, h, background.X, background.Y, background.Size, opts.thickness, paintYellow)

		dest := filepath.Join(outDir, stackFileName(stackDetectedKind, f.Name))
		if err := xisfwrite.WriteRGB(dest, stackWindowID(stackDetectedKind, f.Name), w, h, rgb); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		slog.Info("filter image updated",
			"filter", f.Name,
			"path", dest,
			"artefacts", len(f.Artefacts),
			"missing", len(missing))
	}
	return nil
}

// findBackground reads the clean master of the first filter in L, R, H order
// and chooses a sky window free of union artefacts. It returns the record and
// the frame dimensions the union images must share.
func findBackground(root string, filters []union.Filter, artefacts []report.Artefact) (report.Background, int, int, error) {
	name, path, err := backgroundSource(root, filters)
	if err != nil {
		return report.Background{}, 0, 0, err
	}
	plane, w, h, err := readMonoImage(path)
	if err != nil {
		return report.Background{}, 0, 0, fmt.Errorf("read %s: %w", path, err)
	}
	mask, err := detect.StarMask(plane, w, h, detect.StarMaskOptions{})
	if err != nil {
		return report.Background{}, 0, 0, fmt.Errorf("build star mask for %s: %w", path, err)
	}
	window, err := union.FindBackground(plane, w, h, artefacts, mask, backgroundSize)
	if err != nil {
		return report.Background{}, 0, 0, fmt.Errorf("find background in %s: %w", path, err)
	}
	return report.Background{
		Filter: name,
		X:      window.X,
		Y:      window.Y,
		Size:   window.Size,
		Mean:   window.Mean,
		Noise:  window.Noise,
		SNR:    window.SNR,
		Pixels: window.Pixels,
	}, w, h, nil
}

// backgroundSource returns the preferred filter and its clean master path.
func backgroundSource(root string, filters []union.Filter) (string, string, error) {
	for i := 0; i < len(backgroundOrder); i++ {
		name := string(backgroundOrder[i])
		for _, f := range filters {
			if f.Name != name {
				continue
			}
			path := filepath.Join(root, name, stackFileName(stackCleanKind, name))
			if _, err := os.Stat(path); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					break
				}
				return "", "", fmt.Errorf("stat %s: %w", path, err)
			}
			return name, path, nil
		}
	}
	return "", "", fmt.Errorf(
		"none of the filters %s has a clean master below %s; run scan for one of them first",
		strings.Join(strings.Split(backgroundOrder, ""), ", "), root)
}

// readUnionFilters loads the catalogue of every single-letter filter directory
// under root, in LRGBSHO order.
func readUnionFilters(root string) ([]union.Filter, error) {
	var filters []union.Filter
	for i := 0; i < len(filterLetters); i++ {
		name := string(filterLetters[i])
		path := filepath.Join(root, name, artefactsName)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		artefacts, err := readArtefactsFile(path)
		if err != nil {
			return nil, err
		}
		filters = append(filters, union.Filter{Name: name, Artefacts: artefacts})
	}
	if len(filters) < 2 {
		return nil, fmt.Errorf("union needs at least two filter catalogues below %s, found %d",
			root, len(filters))
	}
	return filters, nil
}

// writeUnionArtefacts writes the merged catalogue with the same schema as the
// per-filter catalogues.
func writeUnionArtefacts(path string, artefacts []report.Artefact) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := report.WriteArtefacts(f, artefacts); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeBackgroundFile writes the single background record.
func writeBackgroundFile(path string, background report.Background) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := report.WriteBackground(f, background); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

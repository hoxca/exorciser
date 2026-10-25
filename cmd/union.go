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

Run scan and annotate for every filter before union.`,
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

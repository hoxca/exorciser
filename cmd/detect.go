package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoxca/exorciser/internal/detect"
	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/xisf"
	"github.com/spf13/cobra"
)

// detectionName is the file written inside the output directory.
const detectionName = "detection.csv"

type detectOptions struct {
	input     string
	outputDir string
	reference string
	filter    string
	window    int
	workers   int
	margin    int
	scales    []float64
	// minSNR is the peak significance at which a frame is called detected.
	minSNR float64
	// coord is the window centre, or nil to use the image centre.
	coord *point
}

func newDetectCmd() *cobra.Command {
	opts := &detectOptions{}
	var coord, scales string

	cmd := &cobra.Command{
		Use:   "detect [path...]",
		Short: "Measure diffuse artefacts against a reference frame",
		Long: `Compare each image with a reference frame and report how significant the
strongest brighter-than-reference diffuse feature in a square window is.
Darker features are ignored.

The two frames are linear, so they differ by an affine transform:
frame ≈ norm_a*reference + norm_b. The transform is fitted on block medians,
so stars, cosmic rays and sub-pixel misalignment do not drag the slope. After
removing the transform and a local background plane, the residual in the window
is matched filtered with Gaussian kernels. A diffuse ghost spread over tens of
pixels is far below the per-pixel noise, but integrating its area turns it into
a measurable peak_snr. The reported ghost_sigma is the scale with the strongest
response, peak_x and peak_y its position, and peak_amp its smoothed amplitude
in pixel units: the kernel-weighted mean residual, always positive because a
darker feature is ignored.

Only features brighter than the reference are scored. A dark artefact can
produce a large response of the opposite sign, but it is ignored, so it never
sets detected, never hides a fainter bright feature in the same window, and
never enters the report as a negative peak_snr. The score is the strongest
positive peak anywhere in the window, not the value at a fixed position:
registration moves a sensor artefact by each frame's alignment offset, so a
fixed-position score would miss the ghost. detected is true when peak_snr
reaches --min-snr, and the report's peak_x and peak_y locate the feature so a
suspicious row can be inspected.

Pixels beyond four times the background noise are masked before filtering,
which removes stars and cosmic rays without a star mask. The background plane is
fitted on the --margin ring outside the window, so the feature itself never
enters the model.

Paths may be directories (searched recursively) or individual .xisf files.
When no path is given the --input directory is used. The reference frame is not
analysed: it is skipped if it appears among the paths. Errors on a single image
are recorded in the report's error column and do not stop the run.

Known limitation: a ghost present in both the frame and the reference cancels
in the residual. The score measures the difference in ghost strength, and
structure common to every row belongs to the reference.

The filter is read from the input directory. A directory ending in
_FILTER-L_mono selects L; L, R, G, B, S, H and O are recognised. Use --filter to
select one filter from a parent directory. A run processes one filter at a time
and writes its report to <output>/<filter>/detection.csv. The report is
semicolon-delimited and uses a decimal comma.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.window <= 0 {
				return fmt.Errorf("--window must be positive, got %d", opts.window)
			}
			if opts.workers <= 0 {
				return fmt.Errorf("--workers must be positive, got %d", opts.workers)
			}
			if opts.minSNR <= 0 || math.IsNaN(opts.minSNR) || math.IsInf(opts.minSNR, 0) {
				return fmt.Errorf("--min-snr must be a positive number, got %v", opts.minSNR)
			}
			if opts.reference == "" {
				return errors.New("--reference is required")
			}
			if filepath.Ext(opts.outputDir) == ".csv" {
				return fmt.Errorf("--output is a directory and the file is always %s, got %q",
					detectionName, opts.outputDir)
			}

			parsed, err := parseScales(scales)
			if err != nil {
				return err
			}
			opts.scales = parsed

			radius := detect.MaxKernelRadius(opts.scales)
			if opts.margin <= radius {
				return fmt.Errorf("--margin must exceed the largest kernel radius %d (3 x the widest --ghost-sigma), got %d",
					radius, opts.margin)
			}

			point, err := parseCoord(coord)
			if err != nil {
				return err
			}
			opts.coord = point

			roots := args
			if len(roots) == 0 {
				roots = []string{opts.input}
			}
			return runDetect(cmd.Context(), roots, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.reference, "reference", "",
		"reference frame compared with every image (required)")
	flags.StringVarP(&opts.input, "input", "i", defaultInput,
		"directory searched for .xisf files when no path is given")
	flags.StringVar(&opts.filter, "filter", "",
		"filter to select from the input (L, R, G, B, S, H or O; default: read from the input directory)")
	flags.StringVarP(&opts.outputDir, "output", "o", "output",
		"root directory receiving <filter>/"+detectionName)
	flags.IntVarP(&opts.window, "window", "w", 128,
		"side length in pixels of the analysed window")
	flags.StringVar(&coord, "coord", "",
		"window centre as x,y in pixels (default: image centre)")
	flags.IntVarP(&opts.workers, "workers", "j", 4,
		"number of images analysed concurrently")
	flags.StringVar(&scales, "ghost-sigma", "4,8,16",
		"matched-filter scales in pixels, comma separated")
	flags.IntVar(&opts.margin, "margin", 64,
		"width in pixels of the background ring around the window")
	flags.Float64Var(&opts.minSNR, "min-snr", 6,
		"peak significance at which a frame is reported as detected")

	return cmd
}

// parseScales parses a comma-separated list of positive Gaussian sigmas.
func parseScales(s string) ([]float64, error) {
	var scales []float64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseFloat(part, 64)
		if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("--ghost-sigma wants positive comma-separated numbers, got %q", s)
		}
		scales = append(scales, v)
	}
	if len(scales) == 0 {
		return nil, errors.New("--ghost-sigma needs at least one scale")
	}
	return scales, nil
}

// point is a pixel position, 0-based from the top-left corner.
type point struct {
	x, y int
}

// parseCoord parses an "x,y" pixel coordinate. An empty string means no
// explicit coordinate and returns a nil point.
func parseCoord(s string) (*point, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}

	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return nil, fmt.Errorf("--coord wants two comma-separated integers, got %q", s)
	}

	var values [2]int
	for i, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("--coord wants two comma-separated integers, got %q", s)
		}
		if n < 0 {
			return nil, fmt.Errorf("--coord values must not be negative, got %d", n)
		}
		values[i] = n
	}
	return &point{x: values[0], y: values[1]}, nil
}

// windowFor returns the window to analyse for a w×h image. An explicit coord is
// rejected rather than shifted when it would cross the frame edge, so every
// analysed row keeps the same window size.
func windowFor(coord *point, size, w, h int) (detect.Window, error) {
	if coord == nil {
		return detect.Centre(w, h, size)
	}
	win := detect.At(coord.x, coord.y, size)
	if err := win.Validate(w, h); err != nil {
		return detect.Window{}, err
	}
	return win, nil
}

func runDetect(ctx context.Context, roots []string, opts *detectOptions) error {
	paths, err := collect(roots)
	if err != nil {
		return err
	}
	paths, skipped := excludeReference(paths, opts.reference)
	if skipped {
		slog.Info("reference skipped from the batch", "file", filepath.Base(opts.reference))
	}
	if len(paths) == 0 {
		return fmt.Errorf("no .xisf files to analyse in %s", strings.Join(roots, ", "))
	}

	filter, paths, err := resolveInputFilter(paths, opts.filter)
	if err != nil {
		return err
	}
	outDir := outputFilterDir(opts.outputDir, filter)

	ref, err := loadReference(opts.reference)
	if err != nil {
		return fmt.Errorf("load reference: %w", err)
	}

	slog.Info("analysing images",
		"filter", filter,
		"files", len(paths),
		"workers", opts.workers,
		"window", opts.window,
		"scales", opts.scales,
		"reference", filepath.Base(opts.reference))

	started := time.Now()
	rows := detectAll(ctx, paths, ref, opts)

	if err := ctx.Err(); err != nil {
		return err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.File
	}
	for _, dup := range duplicateNames(names) {
		slog.Warn("several images share a filename",
			"file", dup.name, "count", dup.count)
	}
	if err := writeDetections(outDir, rows); err != nil {
		return err
	}

	var analysed, failed, detected int
	for _, r := range rows {
		if r.Err != "" {
			failed++
			continue
		}
		analysed++
		if r.Detected {
			detected++
		}
	}
	slog.Info("done",
		"filter", filter,
		"images", analysed,
		"detected", detected,
		"failed", failed,
		"elapsed", time.Since(started).Round(time.Millisecond))
	return nil
}

// reference is the decoded reference plane, shared read-only by every worker.
type reference struct {
	plane  []float32
	width  int
	height int
}

func loadReference(path string) (*reference, error) {
	f, err := xisf.Open(path)
	if err != nil {
		return nil, err
	}
	images := f.Images()
	if len(images) == 0 {
		return nil, errors.New("file contains no images")
	}

	im := images[0]
	if im.Channels() > 1 {
		slog.Warn("reference has several channels, only channel 0 is used",
			"file", filepath.Base(path), "channels", im.Channels())
	}
	plane, err := plane32(im)
	if err != nil {
		return nil, err
	}
	return &reference{plane: plane, width: im.Width(), height: im.Height()}, nil
}

// plane32 returns the first channel as float32. The returned slice aliases im's
// storage; it is read-only from the caller's side.
func plane32(im *xisf.Image) ([]float32, error) {
	if im.SampleFormat == xisf.Float32 {
		return xisf.Plane[float32](im, 0)
	}
	wide, err := im.Float64()
	if err != nil {
		return nil, err
	}
	plane := make([]float32, len(wide))
	for i, v := range wide {
		plane[i] = float32(v)
	}
	return plane, nil
}

// excludeReference drops the reference file from paths, comparing absolute
// paths so a relative --reference still matches the collected files.
func excludeReference(paths []string, refPath string) ([]string, bool) {
	refAbs, err := filepath.Abs(refPath)
	if err != nil {
		refAbs = filepath.Clean(refPath)
	}

	kept := make([]string, 0, len(paths))
	skipped := false
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = filepath.Clean(p)
		}
		if abs == refAbs {
			skipped = true
			continue
		}
		kept = append(kept, p)
	}
	return kept, skipped
}

// detectAll measures paths with a pool of opts.workers goroutines. The
// reference plane is shared read-only, so it is decoded once. Results keep the
// order of paths.
func detectAll(ctx context.Context, paths []string, ref *reference, opts *detectOptions) []report.Row {
	rows := make([]report.Row, len(paths))
	jobs := make(chan int)
	var wg sync.WaitGroup

	for range opts.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := &detector{ref: ref, opts: opts}
			for i := range jobs {
				rows[i] = d.detect(paths[i])
				logDetection(rows[i])
			}
		}()
	}

feed:
	for i := range paths {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	// Drop rows that were never started because of cancellation.
	return slices.DeleteFunc(rows, func(r report.Row) bool { return r.File == "" })
}

// detector holds the per-goroutine state. The reference is read-only and shared;
// a detector is not safe for concurrent use.
type detector struct {
	ref  *reference
	opts *detectOptions
}

func (d *detector) detect(path string) (row report.Row) {
	row.File = filepath.ToSlash(filepath.Base(path))

	started := time.Now()
	defer func() {
		row.DurationMS = time.Since(started).Milliseconds()
	}()

	slog.Debug("opening image", "file", filepath.Base(path))
	f, err := xisf.Open(path)
	if err != nil {
		row.Err = err.Error()
		return row
	}
	images := f.Images()
	if len(images) == 0 {
		row.Err = "file contains no images"
		return row
	}

	im := images[0]
	row.Width, row.Height = im.Width(), im.Height()
	if im.Channels() > 1 {
		slog.Warn("image has several channels, only channel 0 is analysed",
			"file", filepath.Base(path), "channels", im.Channels())
	}
	if row.Width != d.ref.width || row.Height != d.ref.height {
		row.Err = fmt.Sprintf("image is %d×%d but the reference is %d×%d",
			row.Width, row.Height, d.ref.width, d.ref.height)
		return row
	}

	win, err := windowFor(d.opts.coord, d.opts.window, row.Width, row.Height)
	if err != nil {
		row.Err = err.Error()
		return row
	}
	row.WinX, row.WinY, row.WinSize = win.X, win.Y, win.Size

	plane, err := plane32(im)
	if err != nil {
		row.Err = err.Error()
		return row
	}
	res, err := detect.Score(plane, d.ref.plane, row.Width, row.Height, detect.Options{
		Window: win,
		Margin: d.opts.margin,
		Scales: d.opts.scales,
	})
	if err != nil {
		row.Err = err.Error()
		return row
	}

	row.NormA, row.NormB = res.Affine.A, res.Affine.B
	row.Noise = res.Noise
	row.GhostSigma = res.Scale
	row.PeakX, row.PeakY = res.PeakX, res.PeakY
	row.PeakSNR, row.PeakAmp = res.PeakSNR, res.PeakAmp
	row.Clipped = res.Masked
	row.Detected = res.PeakSNR >= d.opts.minSNR
	return row
}

func logDetection(r report.Row) {
	if r.Err != "" {
		slog.Error("image failed", "file", r.File, "err", r.Err)
		return
	}
	slog.Info("image analysed",
		"file", r.File,
		"detected", r.Detected,
		"peak_snr", r.PeakSNR,
		"ghost_sigma", r.GhostSigma,
		"peak_x", r.PeakX,
		"peak_y", r.PeakY,
		"ms", r.DurationMS)
}

// writeDetections writes rows to detection.csv inside dir, creating dir if
// needed.
func writeDetections(dir string, rows []report.Row) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	path := filepath.Join(dir, detectionName)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := report.Write(f, rows); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	slog.Info("report written", "path", path, "rows", len(rows))
	return nil
}

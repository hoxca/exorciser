package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/hoxca/exorciser/internal/detect"
	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/exorciser/internal/stack"
	"github.com/hoxca/xisf"
	"github.com/spf13/cobra"
)

const (
	artefactsName     = "artefacts.csv"
	stackCleanKind    = "stack_clean"
	stackGhostKind    = "stack_ghost"
	stackDetectedKind = "stack_detected"

	// overridableIdentifier tells PixInsight to keep the XISF image id as the
	// window name instead of deriving one from imageType.
	overridableIdentifier = "PCL:OverridableIdentifier"
)

// stackFileName returns the filter-prefixed XISF export name, for example
// L_stack_clean.xisf.
func stackFileName(kind, filter string) string {
	return filter + "_" + kind + ".xisf"
}

// stackWindowID returns the XISF window name, for example clean_L.
func stackWindowID(kind, filter string) string {
	return strings.TrimPrefix(kind, "stack_") + "_" + filter
}

type scanOptions struct {
	report          string
	input           string
	outputDir       string
	filter          string
	count           int
	sigma           float64
	iterations      int
	minFrames       int
	workers         int
	minSNR          float64
	cleanMargin     float64
	polarity        detect.Polarity
	scales          []float64
	margin          int
	corner          int
	starSigma       float64
	starDilation    int
	starHalo        int
	starHaloSmall   int
	starHaloMinArea int
	starHaloMin     float64
}

func newScanCmd() *cobra.Command {
	opts := &scanOptions{}
	var scales, polarity string

	cmd := &cobra.Command{
		Use:   "scan [path...]",
		Short: "Catalogue diffuse artefacts in a stacked difference image",
		Long: `Read a detection table, stack the frames it marks as detected and as clean,
subtract the clean master from the detected one, and search the difference for
every diffuse artefact.

The detected group is ranked by peak_snr and its strongest --count frames are
kept. Every usable clean frame is eligible, except one whose peak_snr lies
within --clean-margin of the detection threshold: for a ghost-free frame the
peak is only a background fluctuation, so its value cannot rank the frame, but
a peak close to the threshold may hide a faint ghost. Clean frames are taken in
report order, up to --count. Every frame is normalised to the first selected
clean frame with an affine transform (scale and offset), then the group is
averaged with per-pixel sigma clipping. The two masters are written as
<filter>_stack_ghost.xisf and <filter>_stack_clean.xisf inside
<output>/<filter>, with XISF window names ghost_<filter> and clean_<filter>.
Their XISF header is inherited from the first frame of the group, with Float32
samples and the template's declared bounds.

The difference is searched with Gaussian matched filters at several scales.
Every local maximum whose absolute signal-to-noise ratio reaches --min-snr and
whose sign matches --polarity is numbered in artefacts.csv, strongest first,
with its registered pixel position.

--polarity both accepts bright and dark features. positive keeps only features
brighter than the clean master; negative keeps only darker ones. Polarity is
applied before peak merging, so a one-sided scan can report peaks that an
opposite-sign peak would otherwise suppress.

Run annotate afterwards to draw the catalogue on an RGB copy of the ghost
master.

Stars are common to both groups, but their subtraction residuals dominate the
difference, so their cores are masked with a dilated local-mean mask built from
the clean master. Wider halo residuals are handled after detection: saturated
components are labelled in the clean master, and a candidate is rejected when
it lies within --star-halo-small of any saturated star or within --star-halo of
a star whose saturated area reaches --star-halo-min-area. The two radii matter
because a big star's halo can be much wider than a small star's. Set
--star-halo=0 to disable this rejection. The mask and the star components are
built from the clean master, so the ghost is absent and survives. The outer
--margin pixels are excluded because registration leaves them poorly
constrained. For the same reason a --corner square at each image corner is
dropped from the catalogue; set --corner=0 to keep it.

Frames are analysed after star alignment, so positions are in registered pixels
and a ghost that is equally present in both groups cancels. A ghost that moves
with each frame's alignment offset is averaged, so its reported centre is the
mean registered position.

The filter is read from the input directory, for example _FILTER-L_mono selects
L. Use --filter to select one filter from a parent directory. The detection
report defaults to <output>/<filter>/detection.csv, and the catalogue and both
masters are written to <output>/<filter>. The reports are semicolon-delimited
and use a decimal comma.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.count < 2 {
				return fmt.Errorf("--count must be at least 2, got %d", opts.count)
			}
			if opts.sigma <= 0 || math.IsNaN(opts.sigma) || math.IsInf(opts.sigma, 0) {
				return fmt.Errorf("--sigma must be a positive number, got %v", opts.sigma)
			}
			if opts.iterations < 1 {
				return fmt.Errorf("--clip-iterations must be positive, got %d", opts.iterations)
			}
			if opts.minFrames < 1 {
				return fmt.Errorf("--min-frames must be positive, got %d", opts.minFrames)
			}
			if opts.minFrames > opts.count {
				return fmt.Errorf("--min-frames %d exceeds --count %d", opts.minFrames, opts.count)
			}
			if opts.workers < 1 {
				return fmt.Errorf("--workers must be positive, got %d", opts.workers)
			}
			if opts.minSNR <= 0 || math.IsNaN(opts.minSNR) || math.IsInf(opts.minSNR, 0) {
				return fmt.Errorf("--min-snr must be a positive number, got %v", opts.minSNR)
			}
			if opts.cleanMargin < 0 || opts.cleanMargin >= 1 ||
				math.IsNaN(opts.cleanMargin) || math.IsInf(opts.cleanMargin, 0) {
				return fmt.Errorf("--clean-margin must be in [0,1), got %v", opts.cleanMargin)
			}
			if opts.margin < 0 {
				return fmt.Errorf("--margin must not be negative, got %d", opts.margin)
			}
			if opts.corner < 0 {
				return fmt.Errorf("--corner must not be negative, got %d", opts.corner)
			}
			if opts.starSigma <= 0 || math.IsNaN(opts.starSigma) || math.IsInf(opts.starSigma, 0) {
				return fmt.Errorf("--star-sigma must be a positive number, got %v", opts.starSigma)
			}
			if opts.starDilation < 0 {
				return fmt.Errorf("--star-dilation must not be negative, got %d", opts.starDilation)
			}
			if opts.starHalo < 0 {
				return fmt.Errorf("--star-halo must not be negative, got %d", opts.starHalo)
			}
			if opts.starHaloSmall < 0 {
				return fmt.Errorf("--star-halo-small must not be negative, got %d", opts.starHaloSmall)
			}
			if opts.starHaloMinArea < 0 {
				return fmt.Errorf("--star-halo-min-area must not be negative, got %d", opts.starHaloMinArea)
			}
			if opts.starHaloMin <= 0 || math.IsNaN(opts.starHaloMin) || math.IsInf(opts.starHaloMin, 0) {
				return fmt.Errorf("--star-halo-min must be a positive number, got %v", opts.starHaloMin)
			}
			if filepath.Ext(opts.outputDir) == ".csv" {
				return fmt.Errorf("--output is a directory and the file is always %s, got %q",
					artefactsName, opts.outputDir)
			}

			parsed, err := parseScales(scales)
			if err != nil {
				return err
			}
			opts.scales = parsed

			polarityMode, err := detect.ParsePolarity(polarity)
			if err != nil {
				return fmt.Errorf("--polarity: %w", err)
			}
			opts.polarity = polarityMode

			roots := args
			if len(roots) == 0 {
				roots = []string{opts.input}
			}
			return runScan(cmd.Context(), roots, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.report, "report", "",
		"detection report produced by detect (default <output>/<filter>/"+detectionName+")")
	flags.StringVarP(&opts.input, "input", "i", defaultInput,
		"directory searched for .xisf files when no path is given")
	flags.StringVar(&opts.filter, "filter", "",
		"filter to select from the input (L, R, G, B, S, H or O; default: read from the input directory)")
	flags.StringVarP(&opts.outputDir, "output", "o", "output",
		"root directory receiving <filter>/"+artefactsName+" and the two masters")
	flags.IntVarP(&opts.count, "count", "n", 10,
		"maximum frames stacked from each group; detected frames are ranked by peak_snr, clean frames are taken in report order")
	flags.Float64Var(&opts.sigma, "sigma", 3,
		"sigma-clipping threshold in robust sigmas")
	flags.IntVar(&opts.iterations, "clip-iterations", 3,
		"number of sigma-clipping passes")
	flags.IntVar(&opts.minFrames, "min-frames", 2,
		"minimum surviving frames per stacked pixel")
	flags.IntVarP(&opts.workers, "workers", "j", 4,
		"number of frames decoded and rows processed concurrently")
	flags.Float64Var(&opts.minSNR, "min-snr", 6,
		"absolute significance at which a difference peak is reported")
	flags.Float64Var(&opts.cleanMargin, "clean-margin", 0.2,
		"fraction of --min-snr within which a clean frame is ambiguous and skipped")
	flags.StringVar(&polarity, "polarity", "both",
		"ghost polarity to report: both, positive or negative")
	flags.StringVar(&scales, "ghost-sigma", "4,8,16",
		"matched-filter scales in pixels, comma separated")
	flags.IntVar(&opts.margin, "margin", 64,
		"border excluded from the scan, in pixels")
	flags.IntVar(&opts.corner, "corner", 80,
		"square ignored at each image corner, in pixels; 0 disables corner rejection")
	flags.Float64Var(&opts.starSigma, "star-sigma", 50,
		"star-mask threshold in local-mean noise sigmas")
	flags.IntVar(&opts.starDilation, "star-dilation", 4,
		"star-mask dilation in pixels")
	flags.IntVar(&opts.starHalo, "star-halo", 220,
		"big-star halo rejection radius in pixels; 0 disables halo rejection")
	flags.IntVar(&opts.starHaloSmall, "star-halo-small", 56,
		"small-star halo rejection radius in pixels")
	flags.IntVar(&opts.starHaloMinArea, "star-halo-min-area", 40,
		"minimum saturated pixels for the big-star halo")
	flags.Float64Var(&opts.starHaloMin, "star-halo-min", 0.9,
		"minimum pixel value for a saturated star")

	return cmd
}

func runScan(ctx context.Context, roots []string, opts *scanOptions) error {
	paths, err := collect(roots)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no .xisf files to scan in %s", strings.Join(roots, ", "))
	}
	filter, paths, err := resolveInputFilter(paths, opts.filter)
	if err != nil {
		return err
	}
	outDir := outputFilterDir(opts.outputDir, filter)

	reportPath := opts.report
	if reportPath == "" {
		reportPath = filepath.Join(outDir, detectionName)
	}
	rows, err := readDetectionRows(reportPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", reportPath, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// A clean frame is one whose peak stayed below the detection threshold.
	// Those within the ambiguity margin of it are skipped rather than stacked.
	ambiguousAt := opts.minSNR * (1 - opts.cleanMargin)
	ghostRows, cleanRows, err := selectScanRows(rows, opts.count, ambiguousAt)
	if err != nil {
		return err
	}

	byName, err := framesByName(paths)
	if err != nil {
		return err
	}
	ghostPaths, err := resolveScanPaths(ghostRows, byName)
	if err != nil {
		return err
	}
	cleanPaths, err := resolveScanPaths(cleanRows, byName)
	if err != nil {
		return err
	}

	slog.Info("frames selected",
		"filter", filter,
		"detected", len(ghostPaths),
		"clean", len(cleanPaths),
		"report", reportPath)
	for _, row := range ghostRows {
		slog.Debug("selected detected frame", "file", row.File, "peak_snr", row.PeakSNR)
	}
	for _, row := range cleanRows {
		slog.Debug("selected clean frame", "file", row.File, "peak_snr", row.PeakSNR)
	}

	// The first clean frame is the common photometric anchor for both groups.
	ref, w, h, err := loadPlane(cleanPaths[0])
	if err != nil {
		return fmt.Errorf("load reference %s: %w", filepath.Base(cleanPaths[0]), err)
	}
	if opts.corner > 0 && 2*opts.corner >= min(w, h) {
		return fmt.Errorf("--corner %d leaves no scan area in a %d×%d image",
			opts.corner, w, h)
	}
	slog.Debug("normalization reference", "file", filepath.Base(cleanPaths[0]))

	rest, err := loadNormalized(ctx, cleanPaths[1:], ref, w, h, opts.workers)
	if err != nil {
		return err
	}
	cleanFrames := make([][]float32, 0, len(cleanPaths))
	cleanFrames = append(cleanFrames, ref)
	cleanFrames = append(cleanFrames, rest...)

	clean, err := stack.Average(cleanFrames, w, h, stack.Options{
		Sigma:      opts.sigma,
		Iterations: opts.iterations,
		MinValid:   opts.minFrames,
		Workers:    opts.workers,
	})
	if err != nil {
		return fmt.Errorf("stack clean frames: %w", err)
	}
	slog.Info("clean master ready",
		"frames", len(cleanFrames),
		"rejected", clean.Rejected,
		"invalid", clean.Invalid)
	cleanPath := filepath.Join(outDir, stackFileName(stackCleanKind, filter))
	if err := writeStack(cleanPath, cleanPaths[0], clean.Plane, w, h, stackWindowID(stackCleanKind, filter)); err != nil {
		return fmt.Errorf("write %s: %w", cleanPath, err)
	}
	slog.Info("master written", "path", cleanPath)
	clear(cleanFrames)
	runtime.GC()

	// Stars are common to both masters, so their residuals are the largest
	// features in the difference. Mask their cores using the clean master,
	// which does not contain the ghost being sought. The wider haloes are
	// rejected from the candidate list below, where their radius can depend on
	// the size of each star.
	mask, err := detect.StarMask(clean.Plane, w, h, detect.StarMaskOptions{
		Sigma:    opts.starSigma,
		Dilation: opts.starDilation,
		Border:   opts.margin,
		Workers:  opts.workers,
	})
	if err != nil {
		return fmt.Errorf("build star mask: %w", err)
	}
	masked := 0
	for _, m := range mask {
		if m {
			masked++
		}
	}
	slog.Info("star mask ready",
		"masked", masked,
		"fraction", float64(masked)/float64(w*h))

	stars, err := detect.StarComponents(clean.Plane, w, h, opts.starHaloMin)
	if err != nil {
		return fmt.Errorf("find star components: %w", err)
	}
	bigStars := 0
	for _, s := range stars {
		if s.Area >= opts.starHaloMinArea {
			bigStars++
		}
	}
	slog.Info("star components ready",
		"stars", len(stars),
		"big", bigStars,
		"min_area", opts.starHaloMinArea)

	if err := ctx.Err(); err != nil {
		return err
	}

	ghostFrames, err := loadNormalized(ctx, ghostPaths, ref, w, h, opts.workers)
	if err != nil {
		return err
	}
	ghost, err := stack.Average(ghostFrames, w, h, stack.Options{
		Sigma:      opts.sigma,
		Iterations: opts.iterations,
		MinValid:   opts.minFrames,
		Workers:    opts.workers,
	})
	if err != nil {
		return fmt.Errorf("stack detected frames: %w", err)
	}
	slog.Info("ghost master ready",
		"frames", len(ghostFrames),
		"rejected", ghost.Rejected,
		"invalid", ghost.Invalid)
	ghostPath := filepath.Join(outDir, stackFileName(stackGhostKind, filter))
	if err := writeStack(ghostPath, ghostPaths[0], ghost.Plane, w, h, stackWindowID(stackGhostKind, filter)); err != nil {
		return fmt.Errorf("write %s: %w", ghostPath, err)
	}
	slog.Info("master written", "path", ghostPath)
	clear(ghostFrames)
	runtime.GC()

	if err := ctx.Err(); err != nil {
		return err
	}

	diff, err := stack.Subtract(ghost.Plane, clean.Plane)
	if err != nil {
		return err
	}
	slog.Info("difference ready",
		"width", w,
		"height", h,
		"polarity", opts.polarity.String())

	found, err := detect.Scan(diff, w, h, detect.ScanOptions{
		Scales:   opts.scales,
		MinSNR:   opts.minSNR,
		Polarity: opts.polarity,
		Workers:  opts.workers,
		Mask:     mask,
	})
	if err != nil {
		return fmt.Errorf("scan difference: %w", err)
	}
	if opts.corner > 0 {
		var rejected int
		found, rejected = detect.RejectCorners(found, w, h, opts.corner)
		slog.Info("corner candidates rejected",
			"rejected", rejected,
			"remaining", len(found),
			"size", opts.corner)
	}
	if opts.starHalo > 0 {
		var rejected int
		found, rejected = detect.RejectStarHaloes(
			found, stars,
			float64(opts.starHaloSmall), float64(opts.starHalo),
			opts.starHaloMinArea)
		slog.Info("star halo candidates rejected",
			"rejected", rejected,
			"remaining", len(found))
	}

	artefacts := make([]report.Artefact, len(found))
	for i, a := range found {
		artefacts[i] = report.Artefact{
			N:     i + 1,
			X:     a.X,
			Y:     a.Y,
			SNR:   a.SNR,
			Amp:   a.Amp,
			Scale: a.Scale,
		}
	}
	if err := writeArtefacts(outDir, artefacts); err != nil {
		return err
	}
	slog.Info("done",
		"filter", filter,
		"artefacts", len(artefacts),
		"width", w,
		"height", h)
	return nil
}

// selectScanRows picks the frames to stack.
//
// The detected group keeps the strongest peaks: there peak_snr measures ghost
// strength, so it ranks the frames. The clean group is different. A frame is
// called clean because its peak stayed below the detection threshold, so its
// peak_snr is only the largest background fluctuation and cannot rank it
// against the other clean frames. Every usable clean frame is therefore
// eligible, except one whose peak_snr reaches ambiguousAt: a peak that close
// to the threshold may hide a faint ghost, and that is a contamination risk
// rather than a quality ranking. Clean frames are taken in report order.
//
// Both groups are capped at count. ambiguousAt is the absolute peak_snr at or
// above which a clean frame is skipped; pass the detection threshold itself to
// disable the guard.
func selectScanRows(rows []report.Row, count int, ambiguousAt float64) (ghost, clean []report.Row, err error) {
	seen := make(map[string]bool)
	ambiguous := 0
	for _, row := range rows {
		if row.Err != "" {
			continue
		}
		if math.IsNaN(row.PeakSNR) || math.IsInf(row.PeakSNR, 0) {
			slog.Warn("row has no usable peak_snr", "file", row.File)
			continue
		}
		if seen[row.File] {
			slog.Warn("duplicate report row ignored", "file", row.File)
			continue
		}
		seen[row.File] = true
		if row.Detected {
			ghost = append(ghost, row)
			continue
		}
		if row.PeakSNR >= ambiguousAt {
			ambiguous++
			slog.Debug("clean frame is too close to the detection threshold",
				"file", row.File, "peak_snr", row.PeakSNR, "threshold", ambiguousAt)
			continue
		}
		clean = append(clean, row)
	}

	// A detected row is ranked by its peak, strongest first. The clean group is
	// ordered by name so the selection is deterministic.
	strength := func(a, b report.Row) int {
		switch {
		case a.PeakSNR > b.PeakSNR:
			return -1
		case a.PeakSNR < b.PeakSNR:
			return 1
		default:
			return strings.Compare(a.File, b.File)
		}
	}
	slices.SortFunc(ghost, strength)
	slices.SortFunc(clean, func(a, b report.Row) int { return strings.Compare(a.File, b.File) })

	if ambiguous > 0 {
		slog.Info("ambiguous clean frames skipped",
			"frames", ambiguous, "peak_snr", ambiguousAt)
	}

	if len(ghost) < 2 {
		return nil, nil, fmt.Errorf("detection report has %d usable detected frames, need at least 2", len(ghost))
	}
	if len(clean) < 2 {
		return nil, nil, fmt.Errorf(
			"detection report has %d usable clean frames below the ambiguity threshold %.2f, need at least 2; lower --clean-margin or --min-snr",
			len(clean), ambiguousAt)
	}

	if len(ghost) > count {
		ghost = ghost[:count]
	} else if len(ghost) < count {
		slog.Warn("fewer detected frames than requested", "have", len(ghost), "want", count)
	}
	if len(clean) > count {
		slog.Info("clean frames capped", "available", len(clean), "using", count)
		clean = clean[:count]
	} else if len(clean) < count {
		slog.Warn("fewer clean frames than requested", "have", len(clean), "want", count)
	}
	return ghost, clean, nil
}

// framesByName maps basenames to paths. The report identifies frames by name,
// so duplicates make its rows ambiguous and are rejected.
func framesByName(paths []string) (map[string]string, error) {
	byName := make(map[string]string, len(paths))
	duplicates := make(map[string]bool)
	for _, path := range paths {
		name := filepath.Base(path)
		if _, ok := byName[name]; ok {
			duplicates[name] = true
			continue
		}
		byName[name] = path
	}
	if len(duplicates) > 0 {
		names := make([]string, 0, len(duplicates))
		for name := range duplicates {
			names = append(names, name)
		}
		slices.Sort(names)
		return nil, fmt.Errorf("several collected frames share a name, the report would be ambiguous: %s",
			strings.Join(names, ", "))
	}
	return byName, nil
}

// resolveScanPaths maps report rows to the collected files, rejecting rows
// whose frame is missing.
func resolveScanPaths(rows []report.Row, byName map[string]string) ([]string, error) {
	paths := make([]string, 0, len(rows))
	var missing []string
	for _, row := range rows {
		name := filepath.Base(row.File)
		path, ok := byName[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		paths = append(paths, path)
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("frames named in the report were not found: %s",
			strings.Join(missing, ", "))
	}
	return paths, nil
}

func readDetectionRows(path string) ([]report.Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return report.Read(f)
}

// loadPlane decodes the first channel of the first image in a file.
func loadPlane(path string) ([]float32, int, int, error) {
	f, err := xisf.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	images := f.Images()
	if len(images) == 0 {
		return nil, 0, 0, errors.New("file contains no images")
	}
	im := images[0]
	if im.Channels() > 1 {
		slog.Warn("image has several channels, only channel 0 is used",
			"file", filepath.Base(path), "channels", im.Channels())
	}
	plane, err := plane32(im)
	if err != nil {
		return nil, 0, 0, err
	}
	return plane, im.Width(), im.Height(), nil
}

// loadNormalized decodes paths, normalises each plane to ref, and returns them
// in the same order. Any failure aborts the whole stack.
func loadNormalized(ctx context.Context, paths []string, ref []float32, w, h, workers int) ([][]float32, error) {
	frames := make([][]float32, len(paths))
	errs := make([]error, len(paths))

	jobs := make(chan int)
	var wg sync.WaitGroup
	for range max(1, min(workers, len(paths))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				plane, fw, fh, err := loadPlane(paths[i])
				if err != nil {
					errs[i] = err
					continue
				}
				if fw != w || fh != h {
					errs[i] = fmt.Errorf("image is %d×%d, want %d×%d", fw, fh, w, h)
					continue
				}
				aff, err := stack.Normalize(plane, ref, w, h, 8)
				if err != nil {
					errs[i] = fmt.Errorf("normalize: %w", err)
					continue
				}
				frames[i] = plane
				slog.Debug("frame normalised",
					"file", filepath.Base(paths[i]), "a", aff.A, "b", aff.B)
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

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(paths[i]), err)
		}
	}
	return frames, nil
}

// writeStack writes plane as a Float32 XISF master, using template for the
// header and dimensions.
func writeStack(path, template string, plane []float32, w, h int, id string) error {
	f, err := xisf.Open(template)
	if err != nil {
		return fmt.Errorf("open template: %w", err)
	}
	images := f.Images()
	if len(images) == 0 {
		return errors.New("template contains no images")
	}
	im := images[0]
	if im.Width() != w || im.Height() != h {
		return fmt.Errorf("template is %d×%d, stack is %d×%d", im.Width(), im.Height(), w, h)
	}
	if im.Channels() != 1 {
		return fmt.Errorf("template has %d channels, want 1", im.Channels())
	}

	// bounds is mandatory for a floating-point XISF image. The template's
	// declared range survives the change of sample format; integer templates
	// contribute their natural range, and a bare float template gets 0:1.
	low, high, ok := im.RepresentableRange()
	if !ok {
		low, high = 0, 1
	}
	im.SampleFormat = xisf.Float32
	im.HasBounds = true
	im.Bounds = [2]float64{low, high}
	im.HasOffset = false
	im.ID = id
	im.ImageType = "MasterLight"
	if _, ok := im.Property(overridableIdentifier); !ok {
		if err := im.AddProperty(xisf.Property{
			ID:    overridableIdentifier,
			Type:  "Boolean",
			Value: "false",
		}); err != nil {
			return fmt.Errorf("add %s: %w", overridableIdentifier, err)
		}
	}
	if err := xisf.SetPixels(im, plane); err != nil {
		return err
	}

	return f.WriteFile(path, &xisf.WriteOptions{
		UpdateCreationTime: true,
		CreatorApplication: "exorciser",
		Compression: &xisf.Compression{
			Codec:    xisf.CodecZlib,
			Shuffled: true,
			ItemSize: 4,
		},
	})
}

func writeArtefacts(dir string, artefacts []report.Artefact) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	path := filepath.Join(dir, artefactsName)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := report.WriteArtefacts(f, artefacts); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	slog.Info("artefact report written", "path", path, "artefacts", len(artefacts))
	return nil
}

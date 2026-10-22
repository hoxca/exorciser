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
	artefactsName  = "artefacts.csv"
	stackGhostName = "stack_ghost.xisf"
	stackCleanName = "stack_clean.xisf"
)

type scanOptions struct {
	report       string
	input        string
	outputDir    string
	count        int
	sigma        float64
	iterations   int
	minFrames    int
	workers      int
	minSNR       float64
	scales       []float64
	margin       int
	starSigma    float64
	starDilation int
}

func newScanCmd() *cobra.Command {
	opts := &scanOptions{}
	var scales string

	cmd := &cobra.Command{
		Use:   "scan [path...]",
		Short: "Catalogue diffuse artefacts in a stacked difference image",
		Long: `Read a detection table, stack the frames it marks as detected and as clean,
subtract the clean master from the detected one, and search the difference for
every diffuse artefact.

Both groups are ranked by the absolute peak_snr in the report and the strongest
--count frames of each are kept. Every frame is normalised to the first clean
frame with an affine transform (scale and offset), then the group is averaged
with per-pixel sigma clipping. The two masters are written as stack_ghost.xisf
and stack_clean.xisf inside the output directory. Their XISF header is inherited
from the first frame of the group, with Float32 samples and no bounds.

The difference is searched with Gaussian matched filters at several scales.
Every local maximum whose absolute signal-to-noise ratio reaches --min-snr is
numbered in artefacts.csv, strongest first, with its registered pixel position.
Stars are common to both groups, but their subtraction residuals dominate the
difference, so they are masked with a dilated local-mean mask built from the
clean master. The ghost is absent from that master and survives. The outer
--margin pixels are excluded because registration leaves them poorly
constrained.

Frames are analysed after star alignment, so positions are in registered pixels
and a ghost that is equally present in both groups cancels. A ghost that moves
with each frame's alignment offset is averaged, so its reported centre is the
mean registered position.

The reports are semicolon-delimited and use a decimal comma.`,
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
			if opts.margin < 0 {
				return fmt.Errorf("--margin must not be negative, got %d", opts.margin)
			}
			if opts.starSigma <= 0 || math.IsNaN(opts.starSigma) || math.IsInf(opts.starSigma, 0) {
				return fmt.Errorf("--star-sigma must be a positive number, got %v", opts.starSigma)
			}
			if opts.starDilation < 0 {
				return fmt.Errorf("--star-dilation must not be negative, got %d", opts.starDilation)
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

			roots := args
			if len(roots) == 0 {
				roots = []string{opts.input}
			}
			return runScan(cmd.Context(), roots, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.report, "report", filepath.Join("output", detectionName),
		"detection report produced by detect")
	flags.StringVarP(&opts.input, "input", "i", defaultInput,
		"directory searched for .xisf files when no path is given")
	flags.StringVarP(&opts.outputDir, "output", "o", "output",
		"directory receiving "+artefactsName+" and the two masters")
	flags.IntVarP(&opts.count, "count", "n", 10,
		"strongest frames taken from each group")
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
	flags.StringVar(&scales, "ghost-sigma", "4,8,16",
		"matched-filter scales in pixels, comma separated")
	flags.IntVar(&opts.margin, "margin", 64,
		"border excluded from the scan, in pixels")
	flags.Float64Var(&opts.starSigma, "star-sigma", 50,
		"star-mask threshold in local-mean noise sigmas")
	flags.IntVar(&opts.starDilation, "star-dilation", 4,
		"star-mask dilation in pixels")

	return cmd
}

func runScan(ctx context.Context, roots []string, opts *scanOptions) error {
	if err := os.MkdirAll(opts.outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	rows, err := readDetectionRows(opts.report)
	if err != nil {
		return fmt.Errorf("read %s: %w", opts.report, err)
	}

	ghostRows, cleanRows, err := selectScanRows(rows, opts.count)
	if err != nil {
		return err
	}

	paths, err := collect(roots)
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
		"detected", len(ghostPaths),
		"clean", len(cleanPaths),
		"report", opts.report)
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
	slog.Debug("normalization reference", "file", filepath.Base(cleanPaths[0]))

	rest, err := loadNormalized(ctx, cleanPaths[1:], ref, w, h, opts.workers)
	if err != nil {
		return err
	}
	cleanFrames := make([][]float32, 0, len(cleanPaths))
	cleanFrames = append(cleanFrames, ref)
	cleanFrames = append(cleanFrames, rest...)
	rest = nil

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
	if err := writeStack(filepath.Join(opts.outputDir, stackCleanName), cleanPaths[0], clean.Plane, w, h, "stack_clean"); err != nil {
		return fmt.Errorf("write %s: %w", stackCleanName, err)
	}
	slog.Info("master written", "path", filepath.Join(opts.outputDir, stackCleanName))
	cleanFrames = nil
	runtime.GC()

	// Stars are common to both masters, so their residuals are the largest
	// features in the difference. Mask them using the clean master, which does
	// not contain the ghost being sought.
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
	if err := writeStack(filepath.Join(opts.outputDir, stackGhostName), ghostPaths[0], ghost.Plane, w, h, "stack_ghost"); err != nil {
		return fmt.Errorf("write %s: %w", stackGhostName, err)
	}
	slog.Info("master written", "path", filepath.Join(opts.outputDir, stackGhostName))
	ghostFrames = nil
	runtime.GC()

	if err := ctx.Err(); err != nil {
		return err
	}

	diff, err := stack.Subtract(ghost.Plane, clean.Plane)
	if err != nil {
		return err
	}
	slog.Info("difference ready", "width", w, "height", h)

	found, err := detect.Scan(diff, w, h, detect.ScanOptions{
		Scales:  opts.scales,
		MinSNR:  opts.minSNR,
		Workers: opts.workers,
		Mask:    mask,
	})
	if err != nil {
		return fmt.Errorf("scan difference: %w", err)
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
	if err := writeArtefacts(opts.outputDir, artefacts); err != nil {
		return err
	}
	slog.Info("done",
		"artefacts", len(artefacts),
		"width", w,
		"height", h)
	return nil
}

// selectScanRows keeps the strongest usable rows of each class by |peak_snr|.
func selectScanRows(rows []report.Row, count int) (ghost, clean []report.Row, err error) {
	seen := make(map[string]bool)
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
		} else {
			clean = append(clean, row)
		}
	}

	strength := func(a, b report.Row) int {
		aa, bb := math.Abs(a.PeakSNR), math.Abs(b.PeakSNR)
		switch {
		case aa > bb:
			return -1
		case aa < bb:
			return 1
		default:
			return strings.Compare(a.File, b.File)
		}
	}
	slices.SortFunc(ghost, strength)
	slices.SortFunc(clean, strength)

	if len(ghost) < 2 {
		return nil, nil, fmt.Errorf("detection report has %d usable detected frames, need at least 2", len(ghost))
	}
	if len(clean) < 2 {
		return nil, nil, fmt.Errorf("detection report has %d usable clean frames, need at least 2", len(clean))
	}

	if len(ghost) > count {
		ghost = ghost[:count]
	} else if len(ghost) < count {
		slog.Warn("fewer detected frames than requested", "have", len(ghost), "want", count)
	}
	if len(clean) > count {
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

	im.SampleFormat = xisf.Float32
	im.HasBounds = false
	im.HasOffset = false
	im.ID = id
	im.ImageType = "MasterLight"
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

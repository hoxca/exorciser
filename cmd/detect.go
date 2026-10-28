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
	dirty     string
	filter    string
	window    int
	workers   int
	margin    int
	scales    []float64
	// minSNR is the peak significance at which a frame is called detected.
	minSNR float64
	// minContrast is the smallest peak amplitude, in units of the background
	// noise, at which a frame is called detected. Zero disables the test.
	minContrast float64
	// coord is the window centre, or nil to use the image centre.
	coord *point
	// dirtyCoord is the artefact centre in the dirty frame. It defaults to
	// coord when --dirty-coord is omitted.
	dirtyCoord *point
	// profileRadius is the half-width of the extracted ghost profile.
	profileRadius int
	// searchRadius bounds the peak search around the window centre; zero
	// searches the whole window.
	searchRadius int
	// profile is set by runDetect after the dirty frame is extracted.
	profile *detect.Profile
}

func newDetectCmd() *cobra.Command {
	opts := &detectOptions{}
	var coord, dirtyCoord, scales string

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

A dirty frame can supply the ghost's actual shape. --dirty is resolved like
--reference: a frame index in the selected filter, such as 003 for
..._BIN1_003_..., or a path to an .xisf file. --dirty-coord is the artefact
centre in that frame as x,y; it defaults to --coord when omitted. The dirty
frame is aligned to the clean reference, the residual around that centre is
reduced to a radial median, and the result joins the Gaussian kernels as an
extra matched filter. The report's kernel column names the winner, and
ghost_sigma is 0 when the profile wins. --profile-radius sets the profile
support radius (default 16); --margin must exceed it. The dirty frame is
skipped from the analysis, just like the reference.

The extracted profile is also saved as <output>/<filter>/artefact_profile.png,
a grayscale view of the matched-filter kernel, and <output>/<filter>/artefact.npe,
the same kernel with its extraction metadata in a text format.

Both skipped frames still appear in the report. The reference row carries its
own background noise and detected=false, with no matched-filter fields. The
dirty row carries its extraction noise, its fitted norm, and a peak_snr from
scoring that frame with its own profile; detected is true. That dirty score is
a self-match and is reported for traceability, not used to decide detection.

Only features brighter than the reference are scored. A dark artefact can
produce a large response of the opposite sign, but it is ignored, so it never
sets detected, never hides a fainter bright feature in the same window, and
never enters the report as a negative peak_snr. The score is the strongest
positive peak anywhere in the window, not the value at a fixed position:
registration moves a sensor artefact by each frame's alignment offset, so a
fixed-position score would miss the ghost. Use --search-radius to bound that
search around the window centre. detected is true when peak_snr reaches
--min-snr, and the report's peak_x and peak_y locate the feature so a
suspicious row can be inspected.

--min-contrast adds a second, complementary test: peak_amp / noise must reach
the given value. peak_snr grows with the number of pixels the kernel covers, so
a broad, faint background gradient can pass --min-snr while being invisible.
Contrast measures the feature's peak surface brightness instead. It is most
useful with --dirty, whose compact profile reproduces the artefact's real core;
a broad Gaussian dilutes that core and gives a much smaller peak_amp.

Pixels beyond four times the background noise are masked before the Gaussian
filtering, which removes stars and cosmic rays without a star mask. The profile
is scored on a looser ten-sigma mask: it is matched to the ghost's own shape,
which may have a compact core that the four-sigma mask would remove along with
the stars. Bright stars are still clipped and dilated. The background plane is
fitted on the --margin ring outside the window, so the feature itself never
enters the model.

Paths may be directories (searched recursively) or individual .xisf files.
When no path is given the --input directory is used. --reference is either the
index of a frame in the selected filter, such as 006 for ..._BIN1_006_..., or a
path to an .xisf file. The reference frame is not analysed: it is skipped if it
appears among the paths. Errors on a single image are recorded in the report's
error column and do not stop the run.

Known limitation: a ghost present in both the frame and the reference cancels
in the residual. The score measures the difference in ghost strength, and
structure common to every row belongs to the reference.

The filter is read from the input directory. A directory ending in
_FILTER-L_mono selects L; L, R, G, B, S, H and O are recognised. Use --filter to
select one filter from a parent directory. A run processes one filter at a time
and writes its report to <output>/<filter>/detection.csv. The report is
semicolon-delimited and uses a decimal comma.`,
		Example: `  exorciser detect --reference 011 --dirty 003 --dirty-coord 2463,1989 --coord 2454,1990 --filter B`,
		Args:    cobra.ArbitraryArgs,
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
			if opts.minContrast < 0 || math.IsNaN(opts.minContrast) || math.IsInf(opts.minContrast, 0) {
				return fmt.Errorf("--min-contrast must not be negative, got %v", opts.minContrast)
			}
			if opts.reference == "" {
				return errors.New("--reference is required")
			}
			if filepath.Ext(opts.outputDir) == ".csv" {
				return fmt.Errorf("--output is a directory and the file is always %s, got %q",
					detectionName, opts.outputDir)
			}
			if opts.profileRadius < 1 {
				return fmt.Errorf("--profile-radius must be positive, got %d", opts.profileRadius)
			}
			if opts.searchRadius < 0 {
				return fmt.Errorf("--search-radius must not be negative, got %d", opts.searchRadius)
			}

			parsed, err := parseScales(scales)
			if err != nil {
				return err
			}
			opts.scales = parsed

			point, err := parseCoord(coord)
			if err != nil {
				return err
			}
			opts.coord = point

			dirtyPoint, err := parseCoordFlag(dirtyCoord, "--dirty-coord")
			if err != nil {
				return err
			}
			switch {
			case dirtyPoint != nil && opts.dirty == "":
				return errors.New("--dirty-coord requires --dirty")
			case opts.dirty != "" && dirtyPoint == nil:
				if opts.coord == nil {
					return errors.New("--dirty requires --dirty-coord, or --coord to use as its centre")
				}
				dirtyPoint = opts.coord
			}
			opts.dirtyCoord = dirtyPoint

			radius := detect.MaxKernelRadius(opts.scales)
			if opts.dirty != "" && opts.profileRadius > radius {
				radius = opts.profileRadius
			}
			if opts.margin <= radius {
				return fmt.Errorf("--margin must exceed the largest kernel radius %d, got %d",
					radius, opts.margin)
			}

			roots := args
			if len(roots) == 0 {
				roots = []string{opts.input}
			}
			return runDetect(cmd.Context(), roots, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.reference, "reference", "",
		"reference frame: a frame index (e.g. 006) in the selected filter, or a path to an .xisf file (required)")
	flags.StringVar(&opts.dirty, "dirty", "",
		"dirty frame used to extract a ghost profile: a frame index (e.g. 003) in the selected filter, or a path to an .xisf file")
	flags.StringVar(&dirtyCoord, "dirty-coord", "",
		"artefact centre in the dirty frame as x,y in pixels (default: --coord)")
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
	flags.IntVar(&opts.profileRadius, "profile-radius", 16,
		"radius in pixels of the ghost profile extracted from --dirty")
	flags.IntVar(&opts.searchRadius, "search-radius", 0,
		"bound the peak search to this radius in pixels around the window centre (0: whole window)")
	flags.Float64Var(&opts.minSNR, "min-snr", 6,
		"peak significance at which a frame is reported as detected")
	flags.Float64Var(&opts.minContrast, "min-contrast", 0,
		"minimum peak amplitude in background-noise units (0: only --min-snr decides)")

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

// parseCoord parses an "x,y" pixel coordinate for --coord.
func parseCoord(s string) (*point, error) {
	return parseCoordFlag(s, "--coord")
}

// parseCoordFlag parses an "x,y" pixel coordinate, naming flag in errors. An
// empty string means no explicit coordinate and returns a nil point.
func parseCoordFlag(s, flag string) (*point, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}

	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return nil, fmt.Errorf("%s wants two comma-separated integers, got %q", flag, s)
	}

	var values [2]int
	for i, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("%s wants two comma-separated integers, got %q", flag, s)
		}
		if n < 0 {
			return nil, fmt.Errorf("%s values must not be negative, got %d", flag, n)
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

// maxKernelRadius is the largest matched-filter half-width used by the run.
func maxKernelRadius(opts *detectOptions) int {
	radius := detect.MaxKernelRadius(opts.scales)
	if opts.profile != nil && opts.profile.Radius > radius {
		radius = opts.profile.Radius
	}
	return radius
}

func runDetect(ctx context.Context, roots []string, opts *detectOptions) error {
	paths, err := collect(roots)
	if err != nil {
		return err
	}

	filter, paths, err := resolveInputFilter(paths, opts.filter)
	if err != nil {
		return err
	}

	// Both frames are resolved before either is skipped, so --dirty may name
	// the reference and be rejected explicitly rather than as a missing index.
	refPath, err := resolveFrame(opts.reference, paths, filter, "--reference")
	if err != nil {
		return err
	}

	var dirtyPath string
	if opts.dirty != "" {
		dirtyPath, err = resolveFrame(opts.dirty, paths, filter, "--dirty")
		if err != nil {
			return err
		}
		if samePath(dirtyPath, refPath) {
			return errors.New("--dirty and --reference name the same frame")
		}
	}

	paths, skipped := excludeReference(paths, refPath)
	if skipped {
		slog.Info("reference skipped from the batch", "file", filepath.Base(refPath))
	}
	if dirtyPath != "" {
		paths, skipped = excludeReference(paths, dirtyPath)
		if skipped {
			slog.Info("dirty frame skipped from the batch", "file", filepath.Base(dirtyPath))
		}
	}
	if len(paths) == 0 {
		skipped := "the reference"
		if dirtyPath != "" {
			skipped = "the reference and dirty frame"
		}
		return fmt.Errorf("no frames left to analyse in %s after skipping %s",
			strings.Join(roots, ", "), skipped)
	}
	outDir := outputFilterDir(opts.outputDir, filter)

	ref, err := loadPlaneData(refPath)
	if err != nil {
		return fmt.Errorf("load reference: %w", err)
	}

	var dirtyRow *report.Row
	if dirtyPath != "" {
		if opts.dirtyCoord == nil {
			return errors.New("--dirty requires --dirty-coord")
		}
		dirtyStarted := time.Now()
		dirty, err := loadPlaneData(dirtyPath)
		if err != nil {
			return fmt.Errorf("load dirty frame: %w", err)
		}
		if dirty.width != ref.width || dirty.height != ref.height {
			return fmt.Errorf("dirty frame is %d×%d but the reference is %d×%d",
				dirty.width, dirty.height, ref.width, ref.height)
		}
		profile, err := detect.ExtractProfile(dirty.plane, ref.plane, ref.width, ref.height,
			detect.ProfileOptions{
				X:      opts.dirtyCoord.x,
				Y:      opts.dirtyCoord.y,
				Radius: opts.profileRadius,
				Margin: opts.margin,
			})
		if err != nil {
			return fmt.Errorf("extract ghost profile from %s: %w", filepath.Base(dirtyPath), err)
		}
		opts.profile = &profile
		slog.Info("ghost profile extracted",
			"dirty", filepath.Base(dirtyPath),
			"x", profile.X,
			"y", profile.Y,
			"shift_x", profile.ShiftX,
			"shift_y", profile.ShiftY,
			"radius", profile.Radius,
			"masked", profile.Masked,
			"noise", profile.Noise)

		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
		pngPath, npePath, err := writeArtefactProfile(outDir, profile, filepath.Base(dirtyPath))
		if err != nil {
			return err
		}
		slog.Info("artefact profile written",
			"png", pngPath,
			"kernel", npePath)

		dirtyWin, err := windowFor(opts.dirtyCoord, opts.window, dirty.width, dirty.height)
		if err != nil {
			return fmt.Errorf("dirty window: %w", err)
		}
		res, err := detect.Score(dirty.plane, ref.plane, dirty.width, dirty.height, detect.Options{
			Window:       dirtyWin,
			Margin:       opts.margin,
			Scales:       opts.scales,
			Profile:      opts.profile,
			SearchRadius: opts.searchRadius,
		})
		if err != nil {
			return fmt.Errorf("score dirty frame %s: %w", filepath.Base(dirtyPath), err)
		}
		dirtyRow = &report.Row{
			File:       filepath.ToSlash(filepath.Base(dirtyPath)),
			Synthetic:  true,
			Detected:   true,
			Width:      dirty.width,
			Height:     dirty.height,
			WinX:       dirtyWin.X,
			WinY:       dirtyWin.Y,
			WinSize:    dirtyWin.Size,
			NormA:      res.Affine.A,
			NormB:      res.Affine.B,
			Noise:      profile.Noise,
			GhostSigma: res.Scale,
			Kernel:     res.Kernel,
			PeakX:      res.PeakX,
			PeakY:      res.PeakY,
			PeakSNR:    res.PeakSNR,
			PeakAmp:    res.PeakAmp,
			Clipped:    res.Masked,
			DurationMS: time.Since(dirtyStarted).Milliseconds(),
		}
	}

	refWin, err := windowFor(opts.coord, opts.window, ref.width, ref.height)
	if err != nil {
		return fmt.Errorf("reference window: %w", err)
	}
	refStarted := time.Now()
	refNoise, err := detect.BackgroundNoise(ref.plane, ref.width, ref.height, refWin,
		maxKernelRadius(opts), opts.margin)
	if err != nil {
		return fmt.Errorf("measure reference noise: %w", err)
	}
	synthetic := []report.Row{{
		File:       filepath.ToSlash(filepath.Base(refPath)),
		Synthetic:  true,
		Detected:   false,
		Width:      ref.width,
		Height:     ref.height,
		WinX:       refWin.X,
		WinY:       refWin.Y,
		WinSize:    refWin.Size,
		Noise:      refNoise,
		DurationMS: time.Since(refStarted).Milliseconds(),
	}}
	if dirtyRow != nil {
		synthetic = append(synthetic, *dirtyRow)
	}

	slog.Info("analysing images",
		"filter", filter,
		"files", len(paths),
		"workers", opts.workers,
		"window", opts.window,
		"scales", opts.scales,
		"reference", filepath.Base(refPath),
		"profile", opts.profile != nil)

	started := time.Now()
	rows := detectAll(ctx, paths, ref, opts)
	rows = append(rows, synthetic...)

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
		if r.Synthetic {
			continue
		}
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

// planeData is a decoded single-channel image, shared read-only by every
// worker.
type planeData struct {
	plane  []float32
	width  int
	height int
}

func loadPlaneData(path string) (*planeData, error) {
	plane, w, h, err := loadPlane(path)
	if err != nil {
		return nil, err
	}
	return &planeData{plane: plane, width: w, height: h}, nil
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

// resolveFrame maps a --reference or --dirty value to a file. A value of
// digits is a frame index looked up among the paths of the selected filter;
// anything else is a path and is returned unchanged. flag names the option in
// error messages.
func resolveFrame(value string, paths []string, filter, flag string) (string, error) {
	value = strings.TrimSpace(value)
	if !allDigits(value) {
		return value, nil
	}
	want, err := strconv.Atoi(value)
	if err != nil {
		return value, nil
	}

	var matches []string
	for _, path := range paths {
		if index, ok := frameIndex(filepath.Base(path)); ok && index == want {
			matches = append(matches, path)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no frame with index %s in filter %s; check %s and --filter",
			value, filter, flag)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, len(matches))
		for i, path := range matches {
			names[i] = filepath.Base(path)
		}
		return "", fmt.Errorf("frame index %s is ambiguous in filter %s: %s",
			value, filter, strings.Join(names, ", "))
	}
}

// frameIndex returns the frame sequence number encoded in a filename: the token
// immediately after the BIN token, as in ..._BIN1_006_....
func frameIndex(name string) (int, bool) {
	tokens := strings.Split(name, "_")
	for i := 0; i+1 < len(tokens); i++ {
		bin := tokens[i]
		if len(bin) <= 3 || !strings.EqualFold(bin[:3], "BIN") || !allDigits(bin[3:]) {
			continue
		}
		index, err := strconv.Atoi(tokens[i+1])
		if err != nil {
			return 0, false
		}
		return index, true
	}
	return 0, false
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// excludeReference drops refPath from paths, comparing absolute paths so a
// relative --reference still matches the collected files.
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

// samePath reports whether a and b name the same file, comparing absolute
// paths so a relative option still matches a collected path.
func samePath(a, b string) bool {
	abs := func(p string) string {
		v, err := filepath.Abs(p)
		if err != nil {
			return filepath.Clean(p)
		}
		return v
	}
	return a != "" && b != "" && abs(a) == abs(b)
}

// detectAll measures paths with a pool of opts.workers goroutines. The
// reference plane is shared read-only, so it is decoded once. Results keep the
// order of paths.
func detectAll(ctx context.Context, paths []string, ref *planeData, opts *detectOptions) []report.Row {
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
	ref  *planeData
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
		Window:       win,
		Margin:       d.opts.margin,
		Scales:       d.opts.scales,
		Profile:      d.opts.profile,
		SearchRadius: d.opts.searchRadius,
	})
	if err != nil {
		row.Err = err.Error()
		return row
	}

	row.NormA, row.NormB = res.Affine.A, res.Affine.B
	row.Noise = res.Noise
	row.GhostSigma = res.Scale
	row.Kernel = res.Kernel
	row.PeakX, row.PeakY = res.PeakX, res.PeakY
	row.PeakSNR, row.PeakAmp = res.PeakSNR, res.PeakAmp
	row.Clipped = res.Masked
	row.Detected = exceedsThresholds(res.PeakSNR, res.PeakAmp, res.Noise,
		d.opts.minSNR, d.opts.minContrast)
	return row
}

// exceedsThresholds reports whether a measurement passes both detection
// thresholds. The contrast test compares the kernel-weighted peak amplitude
// with the per-pixel background noise, so it measures how far the feature rises
// above the noise rather than how much area the matched filter integrated.
// A non-positive minContrast disables it.
func exceedsThresholds(snr, amp, noise, minSNR, minContrast float64) bool {
	if snr < minSNR {
		return false
	}
	return minContrast <= 0 || amp >= minContrast*noise
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
		"kernel", r.Kernel,
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

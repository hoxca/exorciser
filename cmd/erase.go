package cmd

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/hoxca/exorciser/internal/detect"
	"github.com/hoxca/exorciser/internal/erase"
	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/xisf"
	"github.com/spf13/cobra"
)

const (
	eraseDefaultInput        = "img/registered"
	eraseDefaultOutput       = "output/registered"
	eraseDefaultProducts     = "output"
	eraseDefaultRadius       = 20
	eraseDefaultFeather      = 4
	eraseDefaultSearchRadius = 24
	eraseDefaultSearchGuard  = 4
	eraseDefaultRadiusScale  = 2.5
	eraseDefaultMinSNR       = 6
	inpaintReportName        = "inpaint.csv"
)

type eraseOptions struct {
	input        string
	output       string
	filter       string
	products     string
	artefacts    string
	background   string
	clean        string
	detection    string
	radius       int
	feather      int
	searchRadius int
	searchGuard  int
	radiusScale  float64
	minSNR       float64
	workers      int
	inplace      bool
}

func newEraseCmd() *cobra.Command {
	opts := &eraseOptions{}

	cmd := &cobra.Command{
		Use:   "erase [path...]",
		Short: "Inpaint catalogued artefacts in the registered frames",
		Long: `Replace every artefact of the union catalogue in the frames that show them,
using the filter's clean master as the source of ghost-free structure.

Every .xisf frame of the selected filter is written to the output directory
with its file name preserved. A frame is only modified when detection.csv
marks it detected; the other frames are copied unchanged.

With --inplace the detected frames are rewritten in the input directory
instead of being copied, and no other frame is touched. Existing files are
replaced, so keep a backup if you need the originals.

The union catalogue defaults to <products>/<filters>/artefacts.csv, where
<filters> is the same directory union writes (for L, R, G and B: LRGB). The
background window is read from background.csv in that directory, and the clean
master from <products>/<filter>/<filter>_stack_clean.xisf.

The catalogue position is only a starting point. For every artefact in a
detected frame, erase searches a circular --search-radius around it with a
matched filter built from the filter's artefact.npe profile, or from a Gaussian
at the catalogue ghost_sigma when no profile exists. Peaks within
--search-guard pixels of that radius are rejected, so a bright object just
outside the expected region cannot be mistaken for the artefact. The frame is
fitted to the clean master once, and each local window gets its own background
plane and star mask. An artefact is skipped when its local peak_snr is below the
detection threshold stored by detect in detect.json; use --min-snr to override
it.

Each repaired disc starts at --radius and grows with ghost_sigma according to
--radius-scale, so a large artefact is not clipped. The frame's local sky is
modelled with a first-order plane, the clean structure is scaled by the frame
to clean affine slope, and Gaussian noise brings the patch to the frame's local
noise. The local annulus excludes star-mask pixels; if it is much noisier than
the global background, erase falls back to the global background window. A
--feather pixel blend inside the rim hides the transition. The added noise is
seeded from the frame name and artefact number, so the result is reproducible.

A per-artefact report is written to <products>/<filter>/inpaint.csv.

Passing clouds invalidate the local background sample: the resulting gradient
leaves a visible disc.

Output defaults to output/registered/<filter>/.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.inplace && cmd.Flags().Changed("output") {
				return errors.New("--inplace and --output cannot be used together")
			}
			if err := opts.validate(); err != nil {
				return err
			}
			roots := args
			if len(roots) == 0 {
				roots = []string{opts.input}
			}
			return runErase(cmd.Context(), roots, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&opts.input, "input", "i", eraseDefaultInput,
		"directory searched for .xisf files when no path is given")
	flags.StringVarP(&opts.output, "output", "o", eraseDefaultOutput,
		"root directory receiving <filter>/ with the repaired frames")
	flags.StringVar(&opts.filter, "filter", "",
		"filter to erase (L, R, G, B, S, H or O; default: read from the input directory)")
	flags.StringVar(&opts.products, "products", eraseDefaultProducts,
		"root directory holding the filter products written by scan, annotate and union")
	flags.StringVar(&opts.artefacts, "artefacts", "",
		"union catalogue to erase (default <products>/<filters>/"+artefactsName+")")
	flags.StringVar(&opts.background, "background", "",
		"background window record (default background.csv beside the catalogue)")
	flags.StringVar(&opts.clean, "clean", "",
		"clean master used as reference (default <products>/<filter>/<filter>_stack_clean.xisf)")
	flags.StringVar(&opts.detection, "detection", "",
		"detection report deciding which frames are modified (default <products>/<filter>/"+detectionName+")")
	flags.IntVar(&opts.radius, "radius", eraseDefaultRadius,
		"minimum radius in pixels of the disc erased around each artefact")
	flags.Float64Var(&opts.radiusScale, "radius-scale", eraseDefaultRadiusScale,
		"per-artefact radius multiplier of ghost_sigma; 0 keeps --radius fixed")
	flags.IntVar(&opts.feather, "feather", eraseDefaultFeather,
		"blend width in pixels inside the disc rim")
	flags.IntVar(&opts.searchRadius, "search-radius", eraseDefaultSearchRadius,
		"radius in pixels searched around each catalogue position")
	flags.IntVar(&opts.searchGuard, "search-guard", eraseDefaultSearchGuard,
		"reject peaks within this distance of the search radius")
	flags.Float64Var(&opts.minSNR, "min-snr", 0,
		"local detection threshold (default: read from detect.json)")
	flags.IntVarP(&opts.workers, "workers", "j", 4,
		"number of frames decoded and written concurrently")
	flags.BoolVar(&opts.inplace, "inplace", false,
		"rewrite the detected frames in the input directory instead of copying them")
	return cmd
}

func (o *eraseOptions) validate() error {
	if o.filter != "" {
		if _, err := normalizeFilter(o.filter); err != nil {
			return fmt.Errorf("--filter: %w", err)
		}
	}
	if !o.inplace && strings.TrimSpace(o.output) == "" {
		return errors.New("--output must not be empty")
	}
	if strings.TrimSpace(o.products) == "" {
		return errors.New("--products must not be empty")
	}
	if o.radius < 1 {
		return fmt.Errorf("--radius must be positive, got %d", o.radius)
	}
	if o.feather < 0 || o.feather > o.radius {
		return fmt.Errorf("--feather must be between 0 and the radius %d, got %d",
			o.radius, o.feather)
	}
	if o.searchRadius < 0 {
		return fmt.Errorf("--search-radius must not be negative, got %d", o.searchRadius)
	}
	if o.searchGuard < 0 {
		return fmt.Errorf("--search-guard must not be negative, got %d", o.searchGuard)
	}
	if o.searchRadius > 0 && o.searchGuard >= o.searchRadius {
		return fmt.Errorf("--search-guard %d must be smaller than --search-radius %d",
			o.searchGuard, o.searchRadius)
	}
	if o.radiusScale < 0 || math.IsNaN(o.radiusScale) || math.IsInf(o.radiusScale, 0) {
		return fmt.Errorf("--radius-scale must not be negative, got %v", o.radiusScale)
	}
	if o.minSNR < 0 || math.IsNaN(o.minSNR) || math.IsInf(o.minSNR, 0) {
		return fmt.Errorf("--min-snr must not be negative, got %v", o.minSNR)
	}
	if o.workers < 1 {
		return fmt.Errorf("--workers must be positive, got %d", o.workers)
	}
	return nil
}

// inpaintRecord is one localisation and repair decision for inpaint.csv.
type inpaintRecord struct {
	Frame       string
	Artefact    int
	CatalogueX  int
	CatalogueY  int
	FoundX      int
	FoundY      int
	SNR         float64
	Radius      int
	Applied     bool
	Calibration string
	Reason      string
}

func runErase(ctx context.Context, roots []string, opts *eraseOptions) error {
	paths, err := collect(roots)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no .xisf files to erase in %s", strings.Join(roots, ", "))
	}
	filter, paths, err := resolveInputFilter(paths, opts.filter)
	if err != nil {
		return err
	}
	if _, err := framesByName(paths); err != nil {
		return err
	}

	products := opts.products
	artefactsPath := opts.artefacts
	if artefactsPath == "" {
		name, err := unionDirectory(products)
		if err != nil {
			return err
		}
		artefactsPath = filepath.Join(products, name, artefactsName)
	}
	backgroundPath := opts.background
	if backgroundPath == "" {
		backgroundPath = filepath.Join(filepath.Dir(artefactsPath), backgroundName)
	}
	cleanPath := opts.clean
	if cleanPath == "" {
		cleanPath = filepath.Join(products, filter, stackFileName(stackCleanKind, filter))
	}
	detectionPath := opts.detection
	if detectionPath == "" {
		detectionPath = filepath.Join(products, filter, detectionName)
	}

	minSNR, err := resolveEraseMinSNR(products, filter, opts.minSNR)
	if err != nil {
		return err
	}
	baseProfile, err := readEraseProfile(products, filter)
	if err != nil {
		return err
	}

	artefacts, err := readArtefactsFile(artefactsPath)
	if err != nil {
		return err
	}
	if len(artefacts) == 0 {
		return fmt.Errorf("%s has no artefacts to erase", artefactsPath)
	}
	background, err := readBackgroundFile(backgroundPath)
	if err != nil {
		return err
	}
	rows, err := readDetectionRows(detectionPath)
	if err != nil {
		return err
	}
	cleanPlane, w, h, err := readMonoImage(cleanPath)
	if err != nil {
		return fmt.Errorf("read clean master: %w", err)
	}
	cleanStats, err := erase.WindowStats(cleanPlane, w, h, background.X, background.Y, background.Size)
	if err != nil {
		return fmt.Errorf("measure clean master background: %w", err)
	}
	starMask, err := detect.StarMask(cleanPlane, w, h, detect.StarMaskOptions{
		Sigma:    50,
		Dilation: 4,
	})
	if err != nil {
		return fmt.Errorf("build star mask: %w", err)
	}

	detected := make(map[string]bool, len(rows))
	listed := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.Err != "" {
			continue
		}
		name := filepath.Base(row.File)
		listed[name] = true
		detected[name] = row.Detected
	}
	var unlisted int
	for _, path := range paths {
		if !listed[filepath.Base(path)] {
			unlisted++
		}
	}
	if unlisted > 0 {
		slog.Warn("frames are not in the detection report and will not be modified",
			"frames", unlisted)
	}

	var outDir string
	if !opts.inplace {
		outDir = filepath.Join(opts.output, filter)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}

	slog.Info("erasing artefacts",
		"filter", filter,
		"frames", len(paths),
		"artefacts", len(artefacts),
		"min_snr", minSNR,
		"search_radius", opts.searchRadius,
		"search_guard", opts.searchGuard,
		"radius", opts.radius,
		"radius_scale", opts.radiusScale,
		"profile", baseProfile != nil,
		"inplace", opts.inplace,
		"background", backgroundPath,
		"clean", cleanPath)

	errs := make([]error, len(paths))
	modified := make([]bool, len(paths))
	records := make([][]inpaintRecord, len(paths))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range max(1, min(opts.workers, len(paths))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				modified[i], records[i], errs[i] = eraseFrame(ctx, paths[i], outDir, detected,
					w, h, cleanPlane, cleanStats, background, artefacts, starMask, baseProfile, minSNR, opts)
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
		return err
	}
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(paths[i]), err)
		}
	}

	reportPath := filepath.Join(products, filter, inpaintReportName)
	var all []inpaintRecord
	for _, rs := range records {
		all = append(all, rs...)
	}
	if err := writeInpaintReport(reportPath, all); err != nil {
		return err
	}

	erased := 0
	for _, ok := range modified {
		if ok {
			erased++
		}
	}
	applied := 0
	for _, rec := range all {
		if rec.Applied {
			applied++
		}
	}
	done := []any{
		"filter", filter,
		"frames", len(paths),
		"erased", erased,
		"unchanged", len(paths) - erased,
		"artefacts", len(artefacts),
		"applied", applied,
		"report", reportPath,
	}
	if opts.inplace {
		done = append(done, "inplace", true)
	} else {
		done = append(done, "output", outDir)
	}
	slog.Info("erase done", done...)
	return nil
}

// resolveEraseMinSNR returns the explicit threshold, the one persisted by
// detect, or the compatibility default.
func resolveEraseMinSNR(products, filter string, override float64) (float64, error) {
	if override > 0 {
		return override, nil
	}
	path := filepath.Join(products, filter, detectMetadataName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("detect.json not found; using compatibility threshold",
				"path", path, "min_snr", eraseDefaultMinSNR)
			return eraseDefaultMinSNR, nil
		}
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	var meta detectMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return 0, fmt.Errorf("decode %s: %w", path, err)
	}
	if meta.MinSNR <= 0 || math.IsNaN(meta.MinSNR) || math.IsInf(meta.MinSNR, 0) {
		return 0, fmt.Errorf("%s has invalid min_snr %v", path, meta.MinSNR)
	}
	slog.Info("detect settings loaded", "path", path, "min_snr", meta.MinSNR)
	return meta.MinSNR, nil
}

// readEraseProfile loads the filter's saved matched-filter profile when one
// exists. Missing files fall back to a Gaussian at each catalogue scale.
func readEraseProfile(products, filter string) (*detect.Profile, error) {
	path := filepath.Join(products, filter, artefactProfileNPE)
	p, err := readArtefactProfile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("artefact profile not found; using Gaussian localisation", "path", path)
			return nil, nil
		}
		return nil, fmt.Errorf("read artefact profile %s: %w", path, err)
	}
	return &p, nil
}

// eraseFrame copies an undetected frame or locates and repairs every catalogue
// artefact in a detected one.
func eraseFrame(ctx context.Context, src, outDir string, detected map[string]bool,
	w, h int, cleanPlane []float32, cleanStats erase.Stats,
	background report.Background, artefacts []report.Artefact,
	starMask []bool, baseProfile *detect.Profile, minSNR float64, opts *eraseOptions,
) (bool, []inpaintRecord, error) {
	name := filepath.Base(src)
	dest := filepath.Join(outDir, name)
	if opts.inplace {
		dest = src
	}
	if !detected[name] {
		if opts.inplace {
			return false, nil, nil
		}
		return false, nil, copyFile(src, dest)
	}
	if err := ctx.Err(); err != nil {
		return false, nil, err
	}

	var mode os.FileMode
	if opts.inplace {
		info, err := os.Stat(src)
		if err != nil {
			return false, nil, err
		}
		mode = info.Mode().Perm()
	}

	plane, fw, fh, err := loadPlane(src)
	if err != nil {
		return false, nil, err
	}
	if fw != w || fh != h {
		return false, nil, fmt.Errorf("frame is %d×%d but the clean master is %d×%d", fw, fh, w, h)
	}

	affine, err := detect.Fit(plane, cleanPlane, w, h, 8)
	if err != nil {
		slog.Warn("frame normalisation failed; using identity", "file", name, "err", err)
		affine = detect.Affine{A: 1, B: 0}
	}
	targetGlobal, err := erase.WindowStats(plane, w, h, background.X, background.Y, background.Size)
	if err != nil {
		return false, nil, fmt.Errorf("measure frame background: %w", err)
	}

	var records []inpaintRecord
	applied := 0
	for i, a := range artefacts {
		rec := inpaintRecord{
			Frame:      name,
			Artefact:   a.N,
			CatalogueX: a.X,
			CatalogueY: a.Y,
		}
		result, radius, err := locateArtefact(plane, cleanPlane, w, h, a, affine,
			starMask, baseProfile, opts)
		if err != nil {
			rec.Reason = err.Error()
			records = append(records, rec)
			continue
		}
		rec.FoundX, rec.FoundY = result.PeakX, result.PeakY
		rec.SNR = result.PeakSNR
		rec.Radius = radius
		if result.PeakSNR < minSNR {
			rec.Reason = "below min-snr"
			records = append(records, rec)
			continue
		}

		rec.Calibration = "global"
		scale := affine.A
		if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
			scale = 1
		}
		inner := radius + opts.feather + 2
		outer := inner + 16
		targetLocal, targetErr := erase.RingPlaneStats(plane, w, h, result.PeakX, result.PeakY, inner, outer, starMask)
		cleanPlaneLocal, cleanErr := erase.RingPlaneStats(cleanPlane, w, h, result.PeakX, result.PeakY, inner, outer, starMask)
		cal := erase.Calibration{
			TargetMean:  targetGlobal.Mean,
			TargetNoise: targetGlobal.Noise,
			CleanMean:   cleanStats.Mean,
			CleanNoise:  cleanStats.Noise,
			Scale:       scale,
		}
		if targetErr == nil && cleanErr == nil {
			// A ring crossing a star halo or a bright neighbour can report a
			// noise several times higher than the frame background. Prefer the
			// global fallback in that case rather than injecting the excess as
			// grain into the repair.
			if targetLocal.Noise <= 3*targetGlobal.Noise && cleanPlaneLocal.Noise <= 3*cleanStats.Noise {
				cal.TargetMean = targetLocal.Plane.C0
				cal.TargetNoise = targetLocal.Noise
				cal.TargetPlane = &targetLocal.Plane
				cal.CleanMean = cleanPlaneLocal.Plane.C0
				cal.CleanNoise = cleanPlaneLocal.Noise
				cal.CleanPlane = &cleanPlaneLocal.Plane
				rec.Calibration = "local"
			} else {
				slog.Debug("local annulus is much noisier than the background; using global fallback",
					"file", name, "artefact", a.N,
					"target_noise", targetLocal.Noise, "target_global", targetGlobal.Noise,
					"clean_noise", cleanPlaneLocal.Noise, "clean_global", cleanStats.Noise)
			}
		} else {
			slog.Debug("local annulus failed; using global background",
				"file", name, "artefact", a.N, "target_err", targetErr, "clean_err", cleanErr)
		}
		rng := rand.New(rand.NewSource(eraseSeed(name, i)))
		if err := erase.Inpaint(plane, cleanPlane, w, h, result.PeakX, result.PeakY,
			radius, opts.feather, cal, rng); err != nil {
			return false, nil, fmt.Errorf("artefact %d at (%d,%d): %w",
				a.N, result.PeakX, result.PeakY, err)
		}
		rec.Applied = true
		applied++
		records = append(records, rec)
	}

	if applied == 0 {
		if opts.inplace {
			return false, records, nil
		}
		return false, records, copyFile(src, dest)
	}
	if err := writeErasedFrame(dest, src, plane); err != nil {
		return false, records, err
	}
	if opts.inplace {
		// WriteFile creates its temporary file with mode 0600; restore the
		// source permissions that the rename replaced.
		if err := os.Chmod(dest, mode); err != nil {
			return false, records, err
		}
	}
	slog.Debug("frame erased",
		"file", name,
		"artefacts", applied,
		"mean", targetGlobal.Mean,
		"noise", targetGlobal.Noise)
	return true, records, nil
}

// localiseWindow picks the largest local window and background margin that
// fit around the catalogue position. Near an image edge it shrinks the window
// and the search radius rather than skipping a real artefact.
func localiseWindow(x, y, w, h, maxRadius, search int) (detect.Window, int, int, error) {
	for _, size := range []int{64, 48, 32, 24} {
		win := detect.At(x, y, size)
		if err := win.Validate(w, h); err != nil {
			continue
		}
		margins := []int{64, 48, 32, maxRadius + 1}
		for _, margin := range margins {
			if margin <= maxRadius {
				continue
			}
			if win.X-margin < 0 || win.Y-margin < 0 ||
				win.X+win.Size+margin > w || win.Y+win.Size+margin > h {
				continue
			}
			searchHere := min(search, size/2)
			if searchHere < 0 {
				searchHere = 0
			}
			return win, margin, searchHere, nil
		}
	}
	return detect.Window{}, 0, 0, errors.New("no local window fits around the catalogue position")
}

// locateArtefact finds one catalogue artefact in the frame around its
// catalogue position. The profile is scaled so its equivalent Gaussian sigma
// matches the catalogue ghost_sigma.
func locateArtefact(frame, clean []float32, w, h int, a report.Artefact,
	affine detect.Affine, starMask []bool, baseProfile *detect.Profile,
	opts *eraseOptions,
) (detect.Result, int, error) {
	scale := a.Scale
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		scale = 8
	}
	maxRadius := detect.MaxKernelRadius([]float64{scale})
	var scaledProfile *detect.Profile
	if baseProfile != nil {
		baseSigma := float64(baseProfile.Radius) / 2
		factor := 1.0
		if baseSigma > 0 {
			factor = scale / baseSigma
		}
		if profile, err := baseProfile.Scaled(factor); err == nil {
			scaledProfile = &profile
			maxRadius = max(maxRadius, profile.Radius)
		}
	}

	win, margin, search, err := localiseWindow(a.X, a.Y, w, h, maxRadius, opts.searchRadius)
	if err != nil {
		return detect.Result{}, 0, err
	}
	scoreOpts := detect.Options{
		Window:           win,
		Margin:           margin,
		Scales:           []float64{scale},
		SearchRadius:     search,
		SearchGuard:      opts.searchGuard,
		Affine:           &affine,
		Mask:             starMask,
		Stride:           8,
		ClipSigma:        100,
		ProfileClipSigma: 100,
	}
	if scaledProfile != nil {
		scoreOpts.Profile = scaledProfile
	}
	result, err := detect.Score(frame, clean, w, h, scoreOpts)
	if err != nil {
		return detect.Result{}, 0, err
	}
	return result, artefactRadius(scale, opts.radius, opts.radiusScale), nil
}

// artefactRadius applies the per-artefact size scale, keeping --radius as the
// minimum for small features.
func artefactRadius(scale float64, minimum int, factor float64) int {
	if factor <= 0 || scale <= 0 {
		return minimum
	}
	r := int(math.Ceil(factor * scale))
	if r < minimum {
		r = minimum
	}
	return r
}

// writeInpaintReport writes the per-frame localisation decisions.
func writeInpaintReport(path string, records []inpaintRecord) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create inpaint report: %w", err)
	}
	cw := csv.NewWriter(f)
	cw.Comma = report.Separator
	header := []string{"frame", "artefact", "catalogue_x", "catalogue_y",
		"found_x", "found_y", "snr", "radius", "applied", "calibration", "reason"}
	if err := cw.Write(header); err != nil {
		_ = f.Close()
		return err
	}
	for _, r := range records {
		row := []string{
			r.Frame,
			strconv.Itoa(r.Artefact),
			strconv.Itoa(r.CatalogueX),
			strconv.Itoa(r.CatalogueY),
			strconv.Itoa(r.FoundX),
			strconv.Itoa(r.FoundY),
			formatReportFloat(r.SNR),
			strconv.Itoa(r.Radius),
			strconv.FormatBool(r.Applied),
			r.Calibration,
			r.Reason,
		}
		if err := cw.Write(row); err != nil {
			_ = f.Close()
			return err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func formatReportFloat(v float64) string {
	return strings.ReplaceAll(strconv.FormatFloat(v, 'f', 3, 64), ".", string(report.DecimalSeparator))
}

// writeErasedFrame writes plane with the source frame's header properties.
func writeErasedFrame(path, template string, plane []float32) error {
	f, err := xisf.Open(template)
	if err != nil {
		return fmt.Errorf("open %s: %w", template, err)
	}
	images := f.Images()
	if len(images) == 0 {
		return fmt.Errorf("%s contains no images", template)
	}
	im := images[0]
	if im.Channels() != 1 {
		return fmt.Errorf("%s has %d channels, want 1", template, im.Channels())
	}
	if len(plane) != im.Width()*im.Height() {
		return fmt.Errorf("%s is %d×%d, plane has %d samples",
			template, im.Width(), im.Height(), len(plane))
	}

	low, high, ok := im.RepresentableRange()
	if !ok {
		low, high = 0, 1
	}
	im.SampleFormat = xisf.Float32
	im.HasBounds = true
	im.Bounds = [2]float64{low, high}
	im.HasOffset = false
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

// unionDirectory returns the union catalogue directory name for the filters
// present under root, in LRGBSHO order.
func unionDirectory(root string) (string, error) {
	filters, err := readUnionFilters(root)
	if err != nil {
		return "", err
	}
	names := make([]string, len(filters))
	for i, f := range filters {
		names[i] = f.Name
	}
	return strings.Join(names, ""), nil
}

func readBackgroundFile(path string) (report.Background, error) {
	f, err := os.Open(path)
	if err != nil {
		return report.Background{}, fmt.Errorf("open background record: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := report.ReadBackground(f)
	if err != nil {
		return report.Background{}, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

// eraseSeed derives the noise seed from the frame name and artefact index, so
// the same frame always receives the same grain.
func eraseSeed(name string, index int) int64 {
	h := fnv.New64a()
	_, _ = io.WriteString(h, name)
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(index))
	_, _ = h.Write(buf[:])
	return int64(h.Sum64())
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dest, info.Mode().Perm())
}

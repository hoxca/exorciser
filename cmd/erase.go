package cmd

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hoxca/exorciser/internal/erase"
	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/xisf"
	"github.com/spf13/cobra"
)

const (
	eraseDefaultInput    = "img/registered"
	eraseDefaultOutput   = "output/registered"
	eraseDefaultProducts = "output"
	eraseDefaultRadius   = 32
	eraseDefaultFeather  = 6
)

type eraseOptions struct {
	input      string
	output     string
	filter     string
	products   string
	artefacts  string
	background string
	clean      string
	detection  string
	radius     int
	feather    int
	workers    int
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

The union catalogue defaults to <products>/<filters>/artefacts.csv, where
<filters> is the same directory union writes (for L, R, G and B: LRGB). The
background window is read from background.csv in that directory, and the clean
master from <products>/<filter>/<filter>_stack_clean.xisf.

For each artefact, a disc of --radius pixels around its centre is replaced by
the matching part of the clean master. The clean samples are shifted onto the
frame's sky level and Gaussian noise is added to bring the patch up to the
frame's noise, both measured in the background window. A --feather pixel blend
inside the rim hides the transition. The added noise is seeded from the frame
name and artefact number, so the result is reproducible.

Output defaults to output/registered/<filter>/.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
		"radius in pixels of the disc erased around each artefact")
	flags.IntVar(&opts.feather, "feather", eraseDefaultFeather,
		"blend width in pixels inside the disc rim")
	flags.IntVarP(&opts.workers, "workers", "j", 4,
		"number of frames decoded and written concurrently")
	return cmd
}

func (o *eraseOptions) validate() error {
	if o.filter != "" {
		if _, err := normalizeFilter(o.filter); err != nil {
			return fmt.Errorf("--filter: %w", err)
		}
	}
	if strings.TrimSpace(o.output) == "" {
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
	if o.workers < 1 {
		return fmt.Errorf("--workers must be positive, got %d", o.workers)
	}
	return nil
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
		slog.Warn("frames are not in the detection report and will be copied unchanged",
			"frames", unlisted)
	}

	outDir := filepath.Join(opts.output, filter)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	slog.Info("erasing artefacts",
		"filter", filter,
		"frames", len(paths),
		"artefacts", len(artefacts),
		"background", backgroundPath,
		"clean", cleanPath)

	errs := make([]error, len(paths))
	modified := make([]bool, len(paths))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range max(1, min(opts.workers, len(paths))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				modified[i], errs[i] = eraseFrame(ctx, paths[i], outDir, detected,
					w, h, cleanPlane, cleanStats, background, artefacts, opts)
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
	erased := 0
	for _, ok := range modified {
		if ok {
			erased++
		}
	}
	slog.Info("erase done",
		"filter", filter,
		"output", outDir,
		"frames", len(paths),
		"erased", erased,
		"copied", len(paths)-erased,
		"artefacts", len(artefacts))
	return nil
}

// eraseFrame copies a clean frame or inpaints every artefact in a detected one.
func eraseFrame(ctx context.Context, src, outDir string, detected map[string]bool,
	w, h int, cleanPlane []float32, cleanStats erase.Stats,
	background report.Background, artefacts []report.Artefact, opts *eraseOptions) (bool, error) {
	name := filepath.Base(src)
	dest := filepath.Join(outDir, name)
	if !detected[name] {
		return false, copyFile(src, dest)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	plane, fw, fh, err := loadPlane(src)
	if err != nil {
		return false, err
	}
	if fw != w || fh != h {
		return false, fmt.Errorf("frame is %d×%d but the clean master is %d×%d", fw, fh, w, h)
	}
	targetStats, err := erase.WindowStats(plane, w, h, background.X, background.Y, background.Size)
	if err != nil {
		return false, fmt.Errorf("measure frame background: %w", err)
	}
	cal := erase.Calibration{
		TargetMean:  targetStats.Mean,
		TargetNoise: targetStats.Noise,
		CleanMean:   cleanStats.Mean,
		CleanNoise:  cleanStats.Noise,
	}
	for i, a := range artefacts {
		rng := rand.New(rand.NewSource(eraseSeed(name, i)))
		if err := erase.Inpaint(plane, cleanPlane, w, h, a.X, a.Y,
			opts.radius, opts.feather, cal, rng); err != nil {
			return false, fmt.Errorf("artefact %d at (%d,%d): %w", a.N, a.X, a.Y, err)
		}
	}
	if err := writeErasedFrame(dest, src, plane); err != nil {
		return false, err
	}
	slog.Debug("frame erased",
		"file", name,
		"artefacts", len(artefacts),
		"mean", targetStats.Mean,
		"noise", targetStats.Noise)
	return true, nil
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

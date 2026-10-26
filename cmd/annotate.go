package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/xisf"
	"github.com/spf13/cobra"
)

type annotateOptions struct {
	image      string
	artefacts  string
	output     string
	filter     string
	arm        int
	thickness  int
	labelScale int
}

func newAnnotateCmd() *cobra.Command {
	opts := &annotateOptions{}

	cmd := &cobra.Command{
		Use:   "annotate",
		Short: "Draw the artefact catalogue on an RGB copy of the ghost master",
		Long: `Read the monochrome ghost master and the artefact catalogue, then write an
RGB image with a red cross at every catalogue position.

The original samples are copied to the red, green and blue channels, so the
ghost appears neutral. Each cross is drawn in pure red and clipped at the image
border. Its catalogue number is printed beside it, also in red: to the right of
the cross when it fits, otherwise to the left. Positions are 0-based registered
pixels, as written by scan.

The filter is selected with --filter. Without it, annotate reads the filter from
explicit --image, --artefacts or --output paths and falls back to L. The default
paths are output/<filter>/<filter>_stack_ghost.xisf,
output/<filter>/artefacts.csv and output/<filter>/<filter>_stack_detected.xisf.
The output XISF window name is detected_<filter>.

The output is a Float32 XISF image with bounds 0:1 and is compressed with zlib.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.validate(); err != nil {
				return err
			}
			return runAnnotate(opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&opts.image, "image", "i", "",
		"monochrome XISF master to annotate (default output/<filter>/<filter>_stack_ghost.xisf)")
	flags.StringVarP(&opts.artefacts, "artefacts", "a", "",
		"artefact catalogue produced by scan (default output/<filter>/"+artefactsName+")")
	flags.StringVarP(&opts.output, "output", "o", "",
		"RGB XISF image to write (default output/<filter>/<filter>_stack_detected.xisf)")
	flags.StringVar(&opts.filter, "filter", "",
		"filter to annotate (L, R, G, B, S, H or O; default: read from paths, otherwise L)")
	flags.IntVar(&opts.arm, "arm", 12, "cross arm length in pixels")
	flags.IntVar(&opts.thickness, "thickness", 3, "cross line thickness in pixels")
	flags.IntVar(&opts.labelScale, "label-scale", 3,
		"catalogue number scale in pixels; 0 hides numbers")
	return cmd
}

func (o *annotateOptions) validate() error {
	if o.filter != "" {
		if _, err := normalizeFilter(o.filter); err != nil {
			return fmt.Errorf("--filter: %w", err)
		}
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
	return o.checkPaths()
}

// checkPaths rejects an output that would overwrite the source image. It skips
// the comparison until resolve has filled in any omitted default paths.
func (o *annotateOptions) checkPaths() error {
	if o.image == "" || o.output == "" {
		return nil
	}
	src, err := filepath.Abs(o.image)
	if err != nil {
		return fmt.Errorf("resolve --image: %w", err)
	}
	dst, err := filepath.Abs(o.output)
	if err != nil {
		return fmt.Errorf("resolve --output: %w", err)
	}
	if src == dst {
		return errors.New("--output must not overwrite --image")
	}
	return nil
}

// resolveFilter chooses the filter from --filter or from the filter directory
// in an explicit path, defaulting to L when no path names one.
func (o *annotateOptions) resolveFilter() (string, error) {
	selected := ""
	if o.filter != "" {
		filter, err := normalizeFilter(o.filter)
		if err != nil {
			return "", fmt.Errorf("--filter: %w", err)
		}
		selected = filter
	}

	paths := []struct {
		name string
		path string
	}{
		{"--image", o.image},
		{"--artefacts", o.artefacts},
		{"--output", o.output},
	}
	for _, p := range paths {
		if p.path == "" {
			continue
		}
		filters := filtersInPath(filepath.Dir(p.path), true)
		if len(filters) > 1 {
			return "", fmt.Errorf("%s %q contains conflicting filters %s",
				p.name, p.path, strings.Join(filters, ", "))
		}
		if len(filters) == 0 {
			continue
		}
		filter := filters[0]
		if selected == "" {
			selected = filter
			continue
		}
		if selected != filter {
			return "", fmt.Errorf("%s %q is filter %s but selected filter is %s",
				p.name, p.path, filter, selected)
		}
	}
	if selected == "" {
		selected = defaultFilter
	}
	return selected, nil
}

// resolve fills the per-filter defaults and returns the selected filter.
func (o *annotateOptions) resolve() (string, error) {
	filter, err := o.resolveFilter()
	if err != nil {
		return "", err
	}
	if o.image == "" {
		o.image = filepath.Join("output", filter, stackFileName(stackGhostKind, filter))
	}
	if o.artefacts == "" {
		o.artefacts = filepath.Join("output", filter, artefactsName)
	}
	if o.output == "" {
		o.output = filepath.Join("output", filter, stackFileName(stackDetectedKind, filter))
	}
	if err := o.checkPaths(); err != nil {
		return "", err
	}
	return filter, nil
}

func runAnnotate(opts *annotateOptions) error {
	filter, err := opts.resolve()
	if err != nil {
		return err
	}
	artefacts, err := readArtefactsFile(opts.artefacts)
	if err != nil {
		return err
	}
	plane, w, h, err := readMonoImage(opts.image)
	if err != nil {
		return err
	}

	rgb, err := composeDetected(plane, w, h, artefacts, opts.arm, opts.thickness, opts.labelScale)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(opts.output), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := writeRGBImage(opts.output, stackWindowID(stackDetectedKind, filter), w, h, rgb); err != nil {
		return fmt.Errorf("write %s: %w", opts.output, err)
	}

	slog.Info("detected image written",
		"filter", filter,
		"path", opts.output,
		"artefacts", len(artefacts),
		"width", w,
		"height", h)
	return nil
}

func readArtefactsFile(path string) ([]report.Artefact, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open artefact catalogue: %w", err)
	}
	defer func() { _ = f.Close() }()

	artefacts, err := report.ReadArtefacts(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return artefacts, nil
}

// readRGBImage reads a three-channel image into the channel-planar layout used
// by writeRGBImage: the first w*h samples are red, then green, then blue.
func readRGBImage(path string) ([]float32, int, int, error) {
	f, err := xisf.Open(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("open image: %w", err)
	}
	images := f.Images()
	if len(images) == 0 {
		return nil, 0, 0, errors.New("image contains no images")
	}
	im := images[0]
	if im.Channels() != 3 {
		return nil, 0, 0, fmt.Errorf("image has %d channels, want 3", im.Channels())
	}
	wide, err := im.Float64()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read image pixels: %w", err)
	}
	w, h := im.Width(), im.Height()
	if len(wide) != 3*w*h {
		return nil, 0, 0, fmt.Errorf("image has %d samples, want %d", len(wide), 3*w*h)
	}
	rgb := make([]float32, len(wide))
	for i, v := range wide {
		rgb[i] = float32(v)
	}
	return rgb, w, h, nil
}

func readMonoImage(path string) ([]float32, int, int, error) {
	f, err := xisf.Open(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("open image: %w", err)
	}
	images := f.Images()
	if len(images) == 0 {
		return nil, 0, 0, errors.New("image contains no images")
	}
	im := images[0]
	if im.Channels() != 1 {
		return nil, 0, 0, fmt.Errorf("image has %d channels, want 1", im.Channels())
	}
	plane, err := plane32(im)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read image pixels: %w", err)
	}
	w, h := im.Width(), im.Height()
	if len(plane) != w*h {
		return nil, 0, 0, fmt.Errorf("image has %d samples, want %d", len(plane), w*h)
	}
	return plane, w, h, nil
}

// paint is an RGB colour used by the annotation overlays.
type paint struct{ r, g, b float32 }

var (
	// paintRed marks artefacts catalogued in this filter.
	paintRed = paint{1, 0, 0}
	// paintYellow marks union artefacts missing from this filter.
	paintYellow = paint{1, 1, 0}
)

// composeDetected copies plane into all three RGB channels, draws a red cross
// at every artefact position and prints its catalogue number beside the cross.
func composeDetected(plane []float32, w, h int, artefacts []report.Artefact, arm, thickness, labelScale int) ([]float32, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("image is %d×%d, want positive dimensions", w, h)
	}
	if arm < 0 {
		return nil, fmt.Errorf("arm length must not be negative, got %d", arm)
	}
	if thickness < 1 {
		return nil, fmt.Errorf("cross thickness must be positive, got %d", thickness)
	}
	if labelScale < 0 {
		return nil, fmt.Errorf("label scale must not be negative, got %d", labelScale)
	}
	n := w * h
	if len(plane) < n {
		return nil, fmt.Errorf("image has %d samples, want at least %d", len(plane), n)
	}

	rgb := make([]float32, 3*n)
	copy(rgb[:n], plane[:n])
	copy(rgb[n:2*n], plane[:n])
	copy(rgb[2*n:], plane[:n])
	for _, a := range artefacts {
		drawCross(rgb, w, h, a.X, a.Y, arm, thickness, paintRed)
	}
	for _, a := range artefacts {
		drawNumber(rgb, w, h, a.N, a.X, a.Y, arm, labelScale, paintRed)
	}
	return rgb, nil
}

func drawCross(rgb []float32, w, h, x, y, arm, thickness int, c paint) {
	n := w * h
	barOffset := -(thickness / 2)
	barLength := thickness
	armLength := 2*arm + 1

	if x0, x1, ok := pixelSpan(x, -arm, armLength, w); ok {
		if y0, y1, ok := pixelSpan(y, barOffset, barLength, h); ok {
			fillColour(rgb, n, w, x0, x1, y0, y1, c)
		}
	}
	if x0, x1, ok := pixelSpan(x, barOffset, barLength, w); ok {
		if y0, y1, ok := pixelSpan(y, -arm, armLength, h); ok {
			fillColour(rgb, n, w, x0, x1, y0, y1, c)
		}
	}
}

// drawSquare draws a hollow square outline of the given size, centred on
// (cx, cy), and clipped at the image border.
func drawSquare(rgb []float32, w, h, cx, cy, size, thickness int, c paint) {
	if size <= 0 || thickness < 1 {
		return
	}
	n := w * h
	half := size / 2
	left, right := cx-half, cx+half
	top, bottom := cy-half, cy+half
	for y := max(0, top); y <= min(h-1, bottom); y++ {
		if y < top+thickness || y > bottom-thickness {
			if x0, x1, ok := clipSpan(left, right, w); ok {
				fillColour(rgb, n, w, x0, x1, y, y, c)
			}
			continue
		}
		if x0, x1, ok := clipSpan(left, left+thickness-1, w); ok {
			fillColour(rgb, n, w, x0, x1, y, y, c)
		}
		if x0, x1, ok := clipSpan(right-thickness+1, right, w); ok {
			fillColour(rgb, n, w, x0, x1, y, y, c)
		}
	}
}

// clipSpan returns the part of [lo, hi] that lies inside [0, limit).
func clipSpan(lo, hi, limit int) (int, int, bool) {
	if hi < 0 || lo >= limit {
		return 0, 0, false
	}
	return max(0, lo), min(limit-1, hi), true
}

// pixelSpan returns the part of [center+start, center+start+length) that lies
// inside [0, limit).
func pixelSpan(center, start, length, limit int) (lo, hi int, ok bool) {
	lo = center + start
	hi = lo + length - 1
	if hi < 0 || lo >= limit {
		return 0, 0, false
	}
	if lo < 0 {
		lo = 0
	}
	if hi >= limit {
		hi = limit - 1
	}
	return lo, hi, true
}

func fillColour(rgb []float32, planeSize, w, x0, x1, y0, y1 int, c paint) {
	for y := y0; y <= y1; y++ {
		row := y * w
		for x := x0; x <= x1; x++ {
			i := row + x
			rgb[i] = c.r
			rgb[planeSize+i] = c.g
			rgb[2*planeSize+i] = c.b
		}
	}
}

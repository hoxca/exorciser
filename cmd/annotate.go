package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/exorciser/internal/xisfwrite"
	"github.com/hoxca/xisf"
	"github.com/spf13/cobra"
)

const stackDetectedName = "stack_detected.xisf"

type annotateOptions struct {
	image      string
	artefacts  string
	output     string
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
	flags.StringVarP(&opts.image, "image", "i", filepath.Join("output", stackGhostName),
		"monochrome XISF master to annotate")
	flags.StringVarP(&opts.artefacts, "artefacts", "a", filepath.Join("output", artefactsName),
		"artefact catalogue produced by scan")
	flags.StringVarP(&opts.output, "output", "o", filepath.Join("output", stackDetectedName),
		"RGB XISF image to write")
	flags.IntVar(&opts.arm, "arm", 12, "cross arm length in pixels")
	flags.IntVar(&opts.thickness, "thickness", 3, "cross line thickness in pixels")
	flags.IntVar(&opts.labelScale, "label-scale", 3,
		"catalogue number scale in pixels; 0 hides numbers")
	return cmd
}

func (o *annotateOptions) validate() error {
	if o.image == "" {
		return errors.New("--image must not be empty")
	}
	if o.artefacts == "" {
		return errors.New("--artefacts must not be empty")
	}
	if o.output == "" {
		return errors.New("--output must not be empty")
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

func runAnnotate(opts *annotateOptions) error {
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
	if err := xisfwrite.WriteRGB(opts.output, "stack_detected", w, h, rgb); err != nil {
		return fmt.Errorf("write %s: %w", opts.output, err)
	}

	slog.Info("detected image written",
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
		drawCross(rgb, w, h, a.X, a.Y, arm, thickness)
	}
	for _, a := range artefacts {
		drawNumber(rgb, w, h, a.N, a.X, a.Y, arm, labelScale)
	}
	return rgb, nil
}

func drawCross(rgb []float32, w, h, x, y, arm, thickness int) {
	n := w * h
	barOffset := -(thickness / 2)
	barLength := thickness
	armLength := 2*arm + 1

	if x0, x1, ok := pixelSpan(x, -arm, armLength, w); ok {
		if y0, y1, ok := pixelSpan(y, barOffset, barLength, h); ok {
			fillRed(rgb, n, w, x0, x1, y0, y1)
		}
	}
	if x0, x1, ok := pixelSpan(x, barOffset, barLength, w); ok {
		if y0, y1, ok := pixelSpan(y, -arm, armLength, h); ok {
			fillRed(rgb, n, w, x0, x1, y0, y1)
		}
	}
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

func fillRed(rgb []float32, planeSize, w, x0, x1, y0, y1 int) {
	for y := y0; y <= y1; y++ {
		row := y * w
		for x := x0; x <= x1; x++ {
			i := row + x
			rgb[i] = 1
			rgb[planeSize+i] = 0
			rgb[2*planeSize+i] = 0
		}
	}
}

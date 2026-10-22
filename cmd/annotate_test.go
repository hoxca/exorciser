package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/xisf"
)

func TestComposeDetected(t *testing.T) {
	const w, h = 32, 24
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = 0.5
	}
	artefacts := []report.Artefact{
		{N: 1, X: 10, Y: 8},
		{N: 2, X: 0, Y: h - 1},
	}

	rgb, err := composeDetected(plane, w, h, artefacts, 3, 1, 1)
	if err != nil {
		t.Fatalf("composeDetected: %v", err)
	}
	if len(rgb) != 3*w*h {
		t.Fatalf("got %d samples, want %d", len(rgb), 3*w*h)
	}

	n := w * h
	checkBackground := func(x, y int) {
		t.Helper()
		i := y*w + x
		if rgb[i] != 0.5 || rgb[n+i] != 0.5 || rgb[2*n+i] != 0.5 {
			t.Errorf("pixel (%d,%d) = %v,%v,%v, want 0.5 in every channel",
				x, y, rgb[i], rgb[n+i], rgb[2*n+i])
		}
	}
	checkRed := func(x, y int) {
		t.Helper()
		i := y*w + x
		if rgb[i] != 1 || rgb[n+i] != 0 || rgb[2*n+i] != 0 {
			t.Errorf("pixel (%d,%d) = %v,%v,%v, want pure red",
				x, y, rgb[i], rgb[n+i], rgb[2*n+i])
		}
	}

	checkBackground(20, 16)
	checkRed(10, 8)
	checkRed(10-3, 8)
	checkRed(10+3, 8)
	checkRed(10, 8-3)
	checkRed(10, 8+3)
	checkBackground(10+4, 8)

	// The catalogue number is printed beside the cross: "1" to the right of
	// (10,8), and "2" to the right of the clipped edge artefact.
	checkRed(15, 11)
	checkRed(4, 23)

	// The edge artefact is clipped without wrapping or panicking.
	checkRed(0, h-1)
	checkRed(1, h-1)
	checkRed(0, h-1-3)
}

func TestAnnotateOptionsValidate(t *testing.T) {
	base := &annotateOptions{
		image:      "output/stack_ghost.xisf",
		artefacts:  "output/artefacts.csv",
		output:     "output/stack_detected.xisf",
		arm:        12,
		thickness:  3,
		labelScale: 3,
	}
	if err := base.validate(); err != nil {
		t.Fatalf("valid options: %v", err)
	}

	same := *base
	same.output = same.image
	if err := same.validate(); err == nil {
		t.Fatal("accepted an output that overwrites the image")
	}

	negative := *base
	negative.arm = -1
	if err := negative.validate(); err == nil {
		t.Fatal("accepted a negative arm")
	}

	thin := *base
	thin.thickness = 0
	if err := thin.validate(); err == nil {
		t.Fatal("accepted a zero thickness")
	}

	smallLabel := *base
	smallLabel.labelScale = -1
	if err := smallLabel.validate(); err == nil {
		t.Fatal("accepted a negative label scale")
	}

	badFilter := *base
	badFilter.filter = "X"
	if err := badFilter.validate(); err == nil {
		t.Fatal("accepted an invalid filter")
	}
}

func TestResolveAnnotateDefaults(t *testing.T) {
	opts := &annotateOptions{}
	filter, err := opts.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if filter != "L" {
		t.Errorf("filter = %q, want L", filter)
	}
	if want := filepath.Join("output", "L", stackGhostName); opts.image != want {
		t.Errorf("image = %q, want %q", opts.image, want)
	}
	if want := filepath.Join("output", "L", artefactsName); opts.artefacts != want {
		t.Errorf("artefacts = %q, want %q", opts.artefacts, want)
	}
	if want := filepath.Join("output", "L", stackDetectedName); opts.output != want {
		t.Errorf("output = %q, want %q", opts.output, want)
	}
}

func TestResolveAnnotateFilter(t *testing.T) {
	tests := []struct {
		name       string
		opts       annotateOptions
		wantFilter string
		wantErr    bool
	}{
		{
			name:       "explicit",
			opts:       annotateOptions{filter: "r"},
			wantFilter: "R",
		},
		{
			name: "from artefacts",
			opts: annotateOptions{
				artefacts: filepath.Join("output", "G", artefactsName),
			},
			wantFilter: "G",
		},
		{
			name: "from image",
			opts: annotateOptions{
				image: filepath.Join("output", "B", stackGhostName),
			},
			wantFilter: "B",
		},
		{
			name: "conflicting paths",
			opts: annotateOptions{
				image:     filepath.Join("output", "L", stackGhostName),
				artefacts: filepath.Join("output", "R", artefactsName),
			},
			wantErr: true,
		},
		{
			name: "explicit conflicts with path",
			opts: annotateOptions{
				filter:    "R",
				artefacts: filepath.Join("output", "L", artefactsName),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			filter, err := opts.resolve()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolve = %q, want error", filter)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if filter != tt.wantFilter {
				t.Fatalf("filter = %q, want %q", filter, tt.wantFilter)
			}
			wantImage := filepath.Join("output", tt.wantFilter, stackGhostName)
			wantArtefacts := filepath.Join("output", tt.wantFilter, artefactsName)
			wantOutput := filepath.Join("output", tt.wantFilter, stackDetectedName)
			if opts.image != wantImage || opts.artefacts != wantArtefacts || opts.output != wantOutput {
				t.Errorf("resolved paths = %q, %q, %q; want %q, %q, %q",
					opts.image, opts.artefacts, opts.output,
					wantImage, wantArtefacts, wantOutput)
			}
		})
	}
}

func TestRunAnnotate(t *testing.T) {
	dir := t.TempDir()
	const w, h = 24, 20
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = 0.25
	}

	image := filepath.Join(dir, "stack_ghost.xisf")
	writeFloatXISF(t, image, w, h, plane)

	artefactsPath := filepath.Join(dir, "artefacts.csv")
	af, err := os.Create(artefactsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := report.WriteArtefacts(af, []report.Artefact{{N: 1, X: 12, Y: 10, SNR: 20}}); err != nil {
		_ = af.Close()
		t.Fatal(err)
	}
	if err := af.Close(); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(dir, "stack_detected.xisf")
	opts := &annotateOptions{
		image:      image,
		artefacts:  artefactsPath,
		output:     output,
		arm:        4,
		thickness:  3,
		labelScale: 1,
	}
	if err := runAnnotate(opts); err != nil {
		t.Fatalf("runAnnotate: %v", err)
	}

	f, err := xisf.Open(output)
	if err != nil {
		t.Fatalf("open annotated image: %v", err)
	}
	im := f.Images()[0]
	if im.Channels() != 3 || im.ColorSpace != xisf.RGB {
		t.Errorf("image is %d channels in %v, want 3 in RGB", im.Channels(), im.ColorSpace)
	}
	if im.Width() != w || im.Height() != h {
		t.Errorf("image is %d×%d, want %d×%d", im.Width(), im.Height(), w, h)
	}
	if low, high, ok := im.RepresentableRange(); !ok || low != 0 || high != 1 {
		t.Errorf("representable range = %v:%v (ok=%v), want 0:1", low, high, ok)
	}

	red, err := xisf.Plane[float32](im, 0)
	if err != nil {
		t.Fatal(err)
	}
	green, err := xisf.Plane[float32](im, 1)
	if err != nil {
		t.Fatal(err)
	}
	blue, err := xisf.Plane[float32](im, 2)
	if err != nil {
		t.Fatal(err)
	}

	i := 10*w + 12
	if red[i] != 1 || green[i] != 0 || blue[i] != 0 {
		t.Errorf("centre = %v,%v,%v, want pure red", red[i], green[i], blue[i])
	}
	i = 13*w + 18
	if red[i] != 1 || green[i] != 0 || blue[i] != 0 {
		t.Errorf("label = %v,%v,%v, want pure red", red[i], green[i], blue[i])
	}
	i = 2*w + 2
	if red[i] != 0.25 || green[i] != 0.25 || blue[i] != 0.25 {
		t.Errorf("background = %v,%v,%v, want 0.25 in every channel", red[i], green[i], blue[i])
	}
}

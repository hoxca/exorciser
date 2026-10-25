package cmd

import (
	"bytes"
	"context"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/xisf"
)

func TestEraseRejectsBadFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "filter", args: []string{"--filter", "X"}},
		{name: "radius", args: []string{"--radius", "0"}},
		{name: "feather negative", args: []string{"--feather", "-1"}},
		{name: "feather above radius", args: []string{"--feather", "33"}},
		{name: "workers", args: []string{"--workers", "0"}},
		{name: "output", args: []string{"-o", " "}},
		{name: "products", args: []string{"--products", " "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newEraseCmd()
			cmd.SetArgs(tt.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			if err := cmd.Execute(); err == nil {
				t.Fatalf("erase accepted %v, want an error", tt.args)
			}
		})
	}
}

func TestRunErase(t *testing.T) {
	const w, h = 128, 128
	const (
		artX, artY   = 64, 64
		starX, starY = 60, 60
	)

	root := t.TempDir()
	products := filepath.Join(root, "products")
	input := filepath.Join(root, "registered",
		"Light_BIN-1_128x128_EXPOSURE-600.00s_FILTER-L_mono")
	if err := os.MkdirAll(input, 0o755); err != nil {
		t.Fatal(err)
	}

	// L and R catalogues make the union directory LR; the union catalogue is
	// what erase reads.
	catalogue := []report.Artefact{{N: 1, X: artX, Y: artY, SNR: 10, Scale: 8}}
	for _, filter := range []string{"L", "R"} {
		writeUnionCatalogue(t, products, filter, catalogue)
	}
	writeUnionCatalogue(t, products, "LR", catalogue)

	writeUnionBackground(t, products, "LR", report.Background{
		Filter: "L", X: 20, Y: 20, Size: 21,
		Mean: 0.01, Noise: 0.001, SNR: 10, Pixels: 441,
	})

	// The clean master carries a star inside the disc, which the erase must
	// transfer to the target frames.
	clean := eraseTestSky(w, h, 0.01, 0.001, 1)
	clean[starY*w+starX] = 0.2
	writeFloatXISF(t, filepath.Join(products, "L", stackFileName(stackCleanKind, "L")),
		w, h, clean)

	rows := []report.Row{
		{File: "a.xisf", Detected: true, Width: w, Height: h, PeakSNR: 20},
		{File: "b.xisf", Detected: false, Width: w, Height: h, PeakSNR: 2},
		{File: "c.xisf", Detected: true, Width: w, Height: h, PeakSNR: 20},
	}
	writeDetectionReport(t, filepath.Join(products, "L", detectionName), rows)

	frames := []string{"a.xisf", "b.xisf", "c.xisf"}
	for i, name := range frames {
		plane := eraseTestSky(w, h, 0.02, 0.002, int64(10+i))
		addEraseGhost(plane, w, artX, artY, 0.05, 4)
		writeFloatXISF(t, filepath.Join(input, name), w, h, plane)
	}
	// A frame absent from the report is copied unchanged.
	unlisted := eraseTestSky(w, h, 0.02, 0.002, 99)
	addEraseGhost(unlisted, w, artX, artY, 0.05, 4)
	writeFloatXISF(t, filepath.Join(input, "d.xisf"), w, h, unlisted)

	opts := &eraseOptions{
		input:    input,
		output:   filepath.Join(root, "out"),
		products: products,
		radius:   8,
		feather:  2,
		workers:  2,
	}
	if err := runErase(context.Background(), []string{input}, opts); err != nil {
		t.Fatalf("runErase: %v", err)
	}

	outDir := filepath.Join(root, "out", "L")
	for _, name := range []string{"a.xisf", "b.xisf", "c.xisf", "d.xisf"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("%s missing from the output: %v", name, err)
		}
	}

	// Clean and unlisted frames are byte-identical copies.
	for _, name := range []string{"b.xisf", "d.xisf"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(input, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s was modified, want an unchanged copy", name)
		}
	}

	// Detected frames lose the ghost and gain the clean master's star.
	for _, name := range []string{"a.xisf", "c.xisf"} {
		plane, fw, fh, err := loadPlane(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if fw != w || fh != h {
			t.Fatalf("%s is %d×%d, want %dx%d", name, fw, fh, w, h)
		}
		if got := plane[artY*w+artX]; got > 0.03 {
			t.Errorf("%s artefact centre = %v, want the ghost removed", name, got)
		}
		if got := plane[starY*w+starX]; got < 0.15 {
			t.Errorf("%s star = %v, want the clean master's star transferred", name, got)
		}
	}

	// The output keeps the source frame's window name.
	f, err := xisf.Open(filepath.Join(outDir, "a.xisf"))
	if err != nil {
		t.Fatalf("open a.xisf: %v", err)
	}
	if got := f.Images()[0].ID; got != "test" {
		t.Errorf("window name = %q, want test", got)
	}
}

func TestEraseNeedsCleanMaster(t *testing.T) {
	root := t.TempDir()
	products := filepath.Join(root, "products")
	input := filepath.Join(root, "registered", "Light_FILTER-L_mono")
	if err := os.MkdirAll(input, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogue := []report.Artefact{{N: 1, X: 10, Y: 10, SNR: 10, Scale: 8}}
	for _, filter := range []string{"L", "R"} {
		writeUnionCatalogue(t, products, filter, catalogue)
	}
	writeUnionCatalogue(t, products, "LR", catalogue)
	writeUnionBackground(t, products, "LR", report.Background{
		Filter: "L", X: 10, Y: 10, Size: 21, Mean: 0.01, Noise: 0.001, SNR: 10, Pixels: 441,
	})
	writeDetectionReport(t, filepath.Join(products, "L", detectionName), []report.Row{
		{File: "a.xisf", Detected: true},
	})

	opts := &eraseOptions{input: input, output: filepath.Join(root, "out"), products: products,
		radius: 8, feather: 2, workers: 1}
	err := runErase(context.Background(), []string{input}, opts)
	if err == nil {
		t.Fatal("runErase without a clean master succeeded, want an error")
	}
}

func eraseTestSky(w, h int, level, noise float64, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = float32(level + rng.NormFloat64()*noise)
	}
	return plane
}

func addEraseGhost(plane []float32, w, cx, cy int, amp, sigma float64) {
	for y := cy - 4*int(sigma); y <= cy+4*int(sigma); y++ {
		for x := cx - 4*int(sigma); x <= cx+4*int(sigma); x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			plane[y*w+x] += float32(amp * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
		}
	}
}

func writeDetectionReport(t *testing.T, path string, rows []report.Row) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := report.Write(f, rows); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeUnionBackground(t *testing.T, root, dir string, background report.Background) {
	t.Helper()
	path := filepath.Join(root, dir, backgroundName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := report.WriteBackground(f, background); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

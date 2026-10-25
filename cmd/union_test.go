package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/exorciser/internal/xisfwrite"
	"github.com/hoxca/xisf"
)

func TestUnionRejectsBadFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "tolerance zero", args: []string{"--tolerance", "0"}},
		{name: "tolerance negative", args: []string{"--tolerance=-1"}},
		{name: "tolerance nan", args: []string{"--tolerance=NaN"}},
		{name: "arm", args: []string{"--arm", "-1"}},
		{name: "thickness", args: []string{"--thickness", "0"}},
		{name: "label scale", args: []string{"--label-scale", "-1"}},
		{name: "output", args: []string{"-o", " "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newUnionCmd()
			cmd.SetArgs(tt.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			if err := cmd.Execute(); err == nil {
				t.Fatalf("union accepted %v, want an error", tt.args)
			}
		})
	}
}

func TestUnionNeedsTwoFilters(t *testing.T) {
	root := t.TempDir()
	writeUnionCatalogue(t, root, "L", []report.Artefact{{N: 1, X: 10, Y: 10}})

	opts := &unionOptions{output: root, tolerance: 16, arm: 2, thickness: 1, labelScale: 1}
	if err := runUnion(context.Background(), opts); err == nil {
		t.Fatal("runUnion with one filter succeeded, want an error")
	}
}

func TestRunUnion(t *testing.T) {
	const w, h = 512, 512

	root := t.TempDir()
	catalogues := map[string][]report.Artefact{
		"L": {{N: 1, X: 100, Y: 100, SNR: 30}, {N: 2, X: 400, Y: 400, SNR: 9}},
		"R": {{N: 1, X: 101, Y: 99, SNR: 20}, {N: 2, X: 400, Y: 401, SNR: 7}},
		"G": {{N: 1, X: 99, Y: 101, SNR: 25}},
		"B": {{N: 1, X: 102, Y: 100, SNR: 15}},
	}
	// Directories that are not single-letter filters must be ignored.
	if err := os.MkdirAll(filepath.Join(root, "LRGB", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "misc"), 0o755); err != nil {
		t.Fatal(err)
	}

	for filter, artefacts := range catalogues {
		writeUnionCatalogue(t, root, filter, artefacts)
		writeUnionDetected(t, root, filter, w, h, artefacts)
	}

	opts := &unionOptions{output: root, tolerance: 16, arm: 2, thickness: 1, labelScale: 1}
	if err := runUnion(context.Background(), opts); err != nil {
		t.Fatalf("runUnion: %v", err)
	}

	unionDir := filepath.Join(root, "LRGB")
	merged := readUnionCatalogue(t, filepath.Join(unionDir, artefactsName))
	if len(merged) != 2 {
		t.Fatalf("union catalogue has %d entries, want 2: %+v", len(merged), merged)
	}
	if merged[0].N != 1 || merged[0].X != 101 || merged[0].Y != 100 {
		t.Errorf("union entry 1 = %+v, want n=1 at (101,100)", merged[0])
	}
	if merged[1].N != 2 || merged[1].X != 400 || merged[1].Y != 401 {
		t.Errorf("union entry 2 = %+v, want n=2 at (400,401)", merged[1])
	}

	// The union entry 2 is missing from G and B, so both images gain a yellow
	// marker there. L and R saw it, so they stay entirely red.
	for _, tc := range []struct {
		filter  string
		missing bool
		redAt   [2]int
	}{
		{"L", false, [2]int{100, 100}},
		{"R", false, [2]int{101, 99}},
		{"G", true, [2]int{99, 101}},
		{"B", true, [2]int{102, 100}},
	} {
		path := filepath.Join(unionDir, stackFileName(stackDetectedKind, tc.filter))
		rgb, gw, gh, err := readRGBImage(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if gw != w || gh != h {
			t.Fatalf("%s is %d×%d, want %dx%d", path, gw, gh, w, h)
		}
		n := w * h
		yellow := 0
		for i := 0; i < n; i++ {
			if rgb[i] == 1 && rgb[n+i] == 1 && rgb[2*n+i] == 0 {
				yellow++
			}
		}
		if !tc.missing {
			if yellow != 0 {
				t.Errorf("%s has %d yellow pixels, want none", tc.filter, yellow)
			}
			continue
		}
		if yellow <= 9 {
			// A bare cross is 9 pixels; the union number adds more.
			t.Errorf("%s has %d yellow pixels, want the cross and its number", tc.filter, yellow)
		}
		core := 401*w + 400
		if rgb[core] != 1 || rgb[n+core] != 1 || rgb[2*n+core] != 0 {
			t.Errorf("%s marker at (400,401) = %v,%v,%v, want yellow",
				tc.filter, rgb[core], rgb[n+core], rgb[2*n+core])
		}
		if p, ok := unionPixel(rgb, w, h, tc.redAt[0], tc.redAt[1]); !ok || p != [3]float32{1, 0, 0} {
			t.Errorf("%s existing red annotation at (%d,%d) was not preserved",
				tc.filter, tc.redAt[0], tc.redAt[1])
		}
	}
}

// TestRunUnionKeepsWindowName checks that the copied images keep the window
// name annotate gave them, rather than the union directory name.
func TestRunUnionKeepsWindowName(t *testing.T) {
	const w, h = 64, 64

	root := t.TempDir()
	catalogues := map[string][]report.Artefact{
		"B": {{N: 1, X: 20, Y: 20, SNR: 10}},
		"R": {{N: 1, X: 21, Y: 20, SNR: 9}},
	}
	for filter, artefacts := range catalogues {
		writeUnionCatalogue(t, root, filter, artefacts)
		writeUnionDetected(t, root, filter, w, h, artefacts)
	}

	opts := &unionOptions{output: root, tolerance: 16, arm: 2, thickness: 1, labelScale: 1}
	if err := runUnion(context.Background(), opts); err != nil {
		t.Fatalf("runUnion: %v", err)
	}

	path := filepath.Join(root, "RB", stackFileName(stackDetectedKind, "B"))
	f, err := xisf.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	images := f.Images()
	if len(images) != 1 {
		t.Fatalf("%s has %d images, want 1", path, len(images))
	}
	if images[0].ID != "detected_B" {
		t.Errorf("window name = %q, want detected_B", images[0].ID)
	}
	if images[0].ImageType != "MasterLight" {
		t.Errorf("image type = %q, want MasterLight", images[0].ImageType)
	}
}

func writeUnionCatalogue(t *testing.T, root, filter string, artefacts []report.Artefact) {
	t.Helper()
	dir := filepath.Join(root, filter)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, artefactsName))
	if err != nil {
		t.Fatal(err)
	}
	if err := report.WriteArtefacts(f, artefacts); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeUnionDetected writes the image annotate would produce for the filter.
func writeUnionDetected(t *testing.T, root, filter string, w, h int, artefacts []report.Artefact) {
	t.Helper()
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = 0.5
	}
	rgb, err := composeDetected(plane, w, h, artefacts, 2, 1, 1)
	if err != nil {
		t.Fatalf("composeDetected: %v", err)
	}
	path := filepath.Join(root, filter, stackFileName(stackDetectedKind, filter))
	if err := xisfwrite.WriteRGB(path, stackWindowID(stackDetectedKind, filter), w, h, rgb); err != nil {
		t.Fatalf("WriteRGB: %v", err)
	}
}

func readUnionCatalogue(t *testing.T, path string) []report.Artefact {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	artefacts, err := report.ReadArtefacts(f)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return artefacts
}

func unionPixel(rgb []float32, w, h, x, y int) ([3]float32, bool) {
	if x < 0 || x >= w || y < 0 || y >= h {
		return [3]float32{}, false
	}
	n := w * h
	i := y*w + x
	return [3]float32{rgb[i], rgb[n+i], rgb[2*n+i]}, true
}

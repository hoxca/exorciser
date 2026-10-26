package cmd

import (
	"context"
	"encoding/csv"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hoxca/exorciser/internal/report"

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
		"L": {{N: 1, X: 100, Y: 100, SNR: 30, Scale: 8}, {N: 2, X: 400, Y: 400, SNR: 9, Scale: 8}},
		"R": {{N: 1, X: 101, Y: 99, SNR: 20, Scale: 8}, {N: 2, X: 400, Y: 401, SNR: 7, Scale: 8}},
		"G": {{N: 1, X: 99, Y: 101, SNR: 25, Scale: 8}},
		"B": {{N: 1, X: 102, Y: 100, SNR: 15, Scale: 8}},
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
	// The background window comes from L, the first filter in L, R, H order.
	writeUnionCleanMaster(t, root, "L", w, h, 0.01, 0.001, 1)

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

	bg := readUnionBackground(t, filepath.Join(unionDir, backgroundName))
	if bg.Filter != "L" {
		t.Errorf("background filter = %q, want L", bg.Filter)
	}
	if bg.Size != backgroundSize {
		t.Errorf("background size = %d, want %d", bg.Size, backgroundSize)
	}
	if bg.X < 32 || bg.X >= w-32 || bg.Y < 32 || bg.Y >= h-32 {
		t.Errorf("background centre = (%d,%d), outside the valid range", bg.X, bg.Y)
	}
	if bg.Mean <= 0 || bg.Noise <= 0 || bg.SNR <= 0 {
		t.Errorf("background metrics = %+v, want positive mean, noise and snr", bg)
	}
	if bg.Pixels < 3169 || bg.Pixels > 65*65 {
		t.Errorf("background surviving pixels = %d, want between 3169 and %d", bg.Pixels, 65*65)
	}
	for _, a := range merged {
		if bg.X-32 <= a.X && a.X <= bg.X+32 && bg.Y-32 <= a.Y && a.Y <= bg.Y+32 {
			t.Errorf("background window centred at (%d,%d) contains artefact %+v", bg.X, bg.Y, a)
		}
	}

	// The union entry 2 is missing from G and B, so both images gain a yellow
	// marker there. L and R saw it, so their annotation stays red.
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

		// The background square is drawn on every filter image.
		for _, p := range [][2]int{
			{bg.X - 32, bg.Y}, {bg.X + 32, bg.Y},
			{bg.X, bg.Y - 32}, {bg.X, bg.Y + 32},
		} {
			if got, ok := unionPixel(rgb, w, h, p[0], p[1]); !ok || got != [3]float32{1, 1, 0} {
				t.Errorf("%s background outline at (%d,%d) = %v, want yellow",
					tc.filter, p[0], p[1], got)
			}
		}

		core, ok := unionPixel(rgb, w, h, 400, 401)
		if !ok {
			t.Fatalf("%s is missing pixel (400,401)", tc.filter)
		}
		if tc.missing {
			if core != [3]float32{1, 1, 0} {
				t.Errorf("%s marker at (400,401) = %v, want yellow", tc.filter, core)
			}
		} else if core != [3]float32{1, 0, 0} {
			t.Errorf("%s pixel at (400,401) = %v, want the existing red annotation", tc.filter, core)
		}

		if p, ok := unionPixel(rgb, w, h, tc.redAt[0], tc.redAt[1]); !ok || p != [3]float32{1, 0, 0} {
			t.Errorf("%s existing red annotation at (%d,%d) was not preserved",
				tc.filter, tc.redAt[0], tc.redAt[1])
		}
	}
}

func TestUnionBackgroundFilterPreference(t *testing.T) {
	tests := []struct {
		name    string
		filters []string
		clean   []string
		want    string
	}{
		{"prefers L", []string{"L", "R", "B"}, []string{"L", "R"}, "L"},
		{"falls back to R", []string{"R", "B"}, []string{"R", "B"}, "R"},
		{"falls back to H", []string{"B", "H"}, []string{"B", "H"}, "H"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runUnionWithClean(t, tt.filters, tt.clean); got != tt.want {
				t.Errorf("background filter = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnionNeedsCleanMaster(t *testing.T) {
	root := t.TempDir()
	for _, filter := range []string{"S", "O"} {
		artefacts := []report.Artefact{{N: 1, X: 100, Y: 100, SNR: 10, Scale: 8}}
		writeUnionCatalogue(t, root, filter, artefacts)
		writeUnionDetected(t, root, filter, 256, 256, artefacts)
		writeUnionCleanMaster(t, root, filter, 256, 256, 0.01, 0.001, 3)
	}

	opts := &unionOptions{output: root, tolerance: 16, arm: 2, thickness: 1, labelScale: 1}
	err := runUnion(context.Background(), opts)
	if err == nil {
		t.Fatal("runUnion without a clean master for L, R or H succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "clean master") {
		t.Errorf("error = %q, want it to name the missing clean master", err)
	}
}

// TestRunUnionKeepsWindowName checks that the copied images keep the window
// name annotate gave them, rather than the union directory name.
func TestRunUnionKeepsWindowName(t *testing.T) {
	const w, h = 256, 256

	root := t.TempDir()
	catalogues := map[string][]report.Artefact{
		"B": {{N: 1, X: 100, Y: 100, SNR: 10, Scale: 8}},
		"R": {{N: 1, X: 101, Y: 100, SNR: 9, Scale: 8}},
	}
	for filter, artefacts := range catalogues {
		writeUnionCatalogue(t, root, filter, artefacts)
		writeUnionDetected(t, root, filter, w, h, artefacts)
	}
	writeUnionCleanMaster(t, root, "R", w, h, 0.01, 0.001, 2)

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

func runUnionWithClean(t *testing.T, filters, clean []string) string {
	t.Helper()
	const w, h = 256, 256
	root := t.TempDir()
	for _, filter := range filters {
		artefacts := []report.Artefact{{N: 1, X: 100, Y: 100, SNR: 10, Scale: 8}}
		writeUnionCatalogue(t, root, filter, artefacts)
		writeUnionDetected(t, root, filter, w, h, artefacts)
	}
	for i, filter := range clean {
		writeUnionCleanMaster(t, root, filter, w, h, 0.01, 0.001, int64(i+1))
	}

	opts := &unionOptions{output: root, tolerance: 16, arm: 2, thickness: 1, labelScale: 1}
	if err := runUnion(context.Background(), opts); err != nil {
		t.Fatalf("runUnion: %v", err)
	}
	path := filepath.Join(root, unionDirName(filters), backgroundName)
	return readUnionBackground(t, path).Filter
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
	if err := writeRGBImage(path, stackWindowID(stackDetectedKind, filter), w, h, rgb); err != nil {
		t.Fatalf("writeRGBImage: %v", err)
	}
}

// writeUnionCleanMaster writes a noisy monochrome clean master, which union
// needs for the background window.
func writeUnionCleanMaster(t *testing.T, root, filter string, w, h int, level, noise float64, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = float32(level + rng.NormFloat64()*noise)
	}
	path := filepath.Join(root, filter, stackFileName(stackCleanKind, filter))
	writeFloatXISF(t, path, w, h, plane)
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

func readUnionBackground(t *testing.T, path string) report.Background {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	reader := csv.NewReader(f)
	reader.Comma = report.Separator
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(records) != 2 {
		t.Fatalf("%s has %d records, want a header and one row", path, len(records))
	}
	if !equalStrings(records[0], report.BackgroundHeader) {
		t.Fatalf("header = %v, want %v", records[0], report.BackgroundHeader)
	}
	row := records[1]

	anInt := func(i int) int {
		v, err := strconv.Atoi(row[i])
		if err != nil {
			t.Fatalf("field %d %q: %v", i, row[i], err)
		}
		return v
	}
	aFloat := func(i int) float64 {
		v, err := strconv.ParseFloat(strings.ReplaceAll(row[i], ",", "."), 64)
		if err != nil {
			t.Fatalf("field %d %q: %v", i, row[i], err)
		}
		return v
	}

	return report.Background{
		Filter: row[0],
		X:      anInt(1),
		Y:      anInt(2),
		Size:   anInt(3),
		Mean:   aFloat(4),
		Noise:  aFloat(5),
		SNR:    aFloat(6),
		Pixels: anInt(7),
	}
}

func unionDirName(filters []string) string {
	var b strings.Builder
	for i := 0; i < len(filterLetters); i++ {
		for _, filter := range filters {
			if filter == string(filterLetters[i]) {
				b.WriteByte(filterLetters[i])
			}
		}
	}
	return b.String()
}

func unionPixel(rgb []float32, w, h, x, y int) ([3]float32, bool) {
	if x < 0 || x >= w || y < 0 || y >= h {
		return [3]float32{}, false
	}
	n := w * h
	i := y*w + x
	return [3]float32{rgb[i], rgb[n+i], rgb[2*n+i]}, true
}

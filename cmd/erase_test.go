package cmd

import (
	"bytes"
	"context"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hoxca/exorciser/internal/detect"
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
		{name: "inplace with output", args: []string{"--inplace", "-o", "out"}},
		{name: "min snr", args: []string{"--min-snr=-1"}},
		{name: "search radius", args: []string{"--search-radius=-1"}},
		{name: "search guard", args: []string{"--search-guard=-1"}},
		{name: "search guard too wide", args: []string{"--search-radius=10", "--search-guard=10"}},
		{name: "radius scale", args: []string{"--radius-scale=-1"}},
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
	const w, h = 256, 256
	const (
		artX, artY = 128, 128
	)

	root := t.TempDir()
	products := filepath.Join(root, "products")
	input := filepath.Join(root, "registered",
		"Light_BIN-1_256x256_EXPOSURE-600.00s_FILTER-L_mono")
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

	clean := eraseTestSky(w, h, 0.01, 0.001, 1)
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
		plane := eraseTargetFromClean(clean, 0.01, int64(10+i))
		addEraseGhost(plane, w, artX, artY, 0.05, 4)
		writeFloatXISF(t, filepath.Join(input, name), w, h, plane)
	}
	// A frame absent from the report is copied unchanged.
	unlisted := eraseTargetFromClean(clean, 0.01, 99)
	addEraseGhost(unlisted, w, artX, artY, 0.05, 4)
	writeFloatXISF(t, filepath.Join(input, "d.xisf"), w, h, unlisted)

	opts := &eraseOptions{
		input:        input,
		output:       filepath.Join(root, "out"),
		products:     products,
		radius:       8,
		feather:      2,
		searchRadius: 24,
		workers:      2,
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

func TestRunEraseInPlace(t *testing.T) {
	const w, h = 256, 256
	const (
		artX, artY = 128, 128
	)

	root := t.TempDir()
	products := filepath.Join(root, "products")
	input := filepath.Join(root, "registered",
		"Light_BIN-1_256x256_EXPOSURE-600.00s_FILTER-L_mono")
	if err := os.MkdirAll(input, 0o755); err != nil {
		t.Fatal(err)
	}

	catalogue := []report.Artefact{{N: 1, X: artX, Y: artY, SNR: 10, Scale: 8}}
	for _, filter := range []string{"L", "R"} {
		writeUnionCatalogue(t, products, filter, catalogue)
	}
	writeUnionCatalogue(t, products, "LR", catalogue)
	writeUnionBackground(t, products, "LR", report.Background{
		Filter: "L", X: 20, Y: 20, Size: 21,
		Mean: 0.01, Noise: 0.001, SNR: 10, Pixels: 441,
	})

	clean := eraseTestSky(w, h, 0.01, 0.001, 1)
	writeFloatXISF(t, filepath.Join(products, "L", stackFileName(stackCleanKind, "L")),
		w, h, clean)

	rows := []report.Row{
		{File: "a.xisf", Detected: true, Width: w, Height: h, PeakSNR: 20},
		{File: "b.xisf", Detected: false, Width: w, Height: h, PeakSNR: 2},
		{File: "c.xisf", Detected: true, Width: w, Height: h, PeakSNR: 20},
	}
	writeDetectionReport(t, filepath.Join(products, "L", detectionName), rows)

	for i, name := range []string{"a.xisf", "b.xisf", "c.xisf"} {
		plane := eraseTargetFromClean(clean, 0.01, int64(10+i))
		addEraseGhost(plane, w, artX, artY, 0.05, 4)
		writeFloatXISF(t, filepath.Join(input, name), w, h, plane)
	}
	unlisted := eraseTargetFromClean(clean, 0.01, 99)
	addEraseGhost(unlisted, w, artX, artY, 0.05, 4)
	writeFloatXISF(t, filepath.Join(input, "d.xisf"), w, h, unlisted)

	// A detected frame with a non-default mode, which the rewrite must keep.
	if err := os.Chmod(filepath.Join(input, "a.xisf"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Snapshot the frames that must not change at all.
	untouched := make(map[string][]byte)
	for _, name := range []string{"b.xisf", "d.xisf"} {
		want, err := os.ReadFile(filepath.Join(input, name))
		if err != nil {
			t.Fatal(err)
		}
		untouched[name] = want
	}

	out := filepath.Join(root, "out")
	opts := &eraseOptions{
		input:        input,
		output:       out,
		products:     products,
		radius:       8,
		feather:      2,
		searchRadius: 24,
		workers:      2,
		inplace:      true,
	}
	if err := runErase(context.Background(), []string{input}, opts); err != nil {
		t.Fatalf("runErase: %v", err)
	}

	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("in-place run created the output directory: %v", err)
	}
	if left, err := filepath.Glob(filepath.Join(input, ".xisf-*")); err != nil {
		t.Fatal(err)
	} else if len(left) > 0 {
		t.Errorf("temporary files left behind: %v", left)
	}

	for name, want := range untouched {
		got, err := os.ReadFile(filepath.Join(input, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s changed, want it untouched", name)
		}
	}

	for _, name := range []string{"a.xisf", "c.xisf"} {
		plane, fw, fh, err := loadPlane(filepath.Join(input, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if fw != w || fh != h {
			t.Fatalf("%s is %d×%d, want %dx%d", name, fw, fh, w, h)
		}
		if got := plane[artY*w+artX]; got > 0.03 {
			t.Errorf("%s artefact centre = %v, want the ghost removed", name, got)
		}
	}

	info, err := os.Stat(filepath.Join(input, "a.xisf"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("in-place rewrite mode = %o, want 640", got)
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

// eraseTargetFromClean builds a registered target frame that shares the clean
// master's sky structure, with the given offset and a little independent noise.
func eraseTargetFromClean(clean []float32, offset float64, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([]float32, len(clean))
	for i, v := range clean {
		out[i] = v + float32(offset) + float32(rng.NormFloat64()*0.0005)
	}
	return out
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

func TestResolveEraseMinSNR(t *testing.T) {
	products := t.TempDir()
	filter := "L"
	if err := os.MkdirAll(filepath.Join(products, filter), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := &detectOptions{
		minSNR:        6.15,
		window:        64,
		margin:        64,
		profileRadius: 16,
		scales:        []float64{4, 8},
		coord:         &point{x: 2467, y: 1970},
	}
	if err := writeDetectMetadata(filepath.Join(products, filter), filter, opts, "ref.xisf", "dirty.xisf"); err != nil {
		t.Fatalf("writeDetectMetadata: %v", err)
	}

	got, err := resolveEraseMinSNR(products, filter, 0)
	if err != nil {
		t.Fatalf("resolveEraseMinSNR: %v", err)
	}
	if got != 6.15 {
		t.Errorf("min_snr = %v, want 6.15", got)
	}
	got, err = resolveEraseMinSNR(products, filter, 9.5)
	if err != nil {
		t.Fatalf("resolveEraseMinSNR override: %v", err)
	}
	if got != 9.5 {
		t.Errorf("override = %v, want 9.5", got)
	}

	if err := os.Remove(filepath.Join(products, filter, detectMetadataName)); err != nil {
		t.Fatal(err)
	}
	got, err = resolveEraseMinSNR(products, filter, 0)
	if err != nil {
		t.Fatalf("resolveEraseMinSNR missing: %v", err)
	}
	if got != eraseDefaultMinSNR {
		t.Errorf("missing metadata fallback = %v, want %v", got, eraseDefaultMinSNR)
	}
}

func TestRepairRadius(t *testing.T) {
	profile20 := &detect.Profile{Radius: 20, Kernel: make([]float64, 41*41)}
	profile8 := &detect.Profile{Radius: 8, Kernel: make([]float64, 17*17)}
	tests := []struct {
		name    string
		scale   float64
		profile *detect.Profile
		minimum int
		factor  float64
		want    int
		source  string
	}{
		{name: "profile", scale: 10, profile: profile20, minimum: 12, factor: 2.5, want: 20, source: "profile"},
		{name: "profile floor", scale: 10, profile: profile8, minimum: 12, factor: 2.5, want: 12, source: "profile"},
		{name: "scale", scale: 10, minimum: 12, factor: 2.5, want: 25, source: "scale"},
		{name: "fixed factor", scale: 10, minimum: 12, factor: 0, want: 12, source: "fixed"},
		{name: "fixed scale", scale: 0, minimum: 12, factor: 2.5, want: 12, source: "fixed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source := repairRadius(tt.scale, tt.profile, tt.minimum, tt.factor)
			if got != tt.want || source != tt.source {
				t.Errorf("repairRadius(%v, %v, %d, %v) = %d,%q, want %d,%q",
					tt.scale, tt.profile, tt.minimum, tt.factor, got, source, tt.want, tt.source)
			}
		})
	}
}

func TestWriteInpaintReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), inpaintReportName)
	records := []inpaintRecord{
		{Frame: "a.xisf", Artefact: 1, CatalogueX: 10, CatalogueY: 20,
			FoundX: 11, FoundY: 19, SNR: 7.25, Radius: 22, RadiusSource: "profile", Applied: true, Calibration: "local"},
		{Frame: "b.xisf", Artefact: 1, CatalogueX: 10, CatalogueY: 20,
			FoundX: 0, FoundY: 0, SNR: 2, Radius: 20, Reason: "below min-snr"},
	}
	if err := writeInpaintReport(path, records); err != nil {
		t.Fatalf("writeInpaintReport: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"frame;artefact;catalogue_x;catalogue_y;found_x;found_y;snr;radius;radius_source;applied;calibration;reason\n",
		"a.xisf;1;10;20;11;19;7,250;22;profile;true;local;\n",
		"b.xisf;1;10;20;0;0;2,000;20;;false;;below min-snr\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report does not contain %q:\n%s", want, text)
		}
	}
}

func TestLocaliseWindowShrinksAtEdge(t *testing.T) {
	win, margin, search, err := localiseWindow(2919, 4050, 4096, 4096, 30, 24)
	if err != nil {
		t.Fatalf("localiseWindow: %v", err)
	}
	if win.Size > 32 {
		t.Errorf("window size = %d, want it reduced near the edge", win.Size)
	}
	if margin <= 30 {
		t.Errorf("margin = %d, want it above the kernel radius", margin)
	}
	if search > win.Size/2 {
		t.Errorf("search radius = %d exceeds half the window %d", search, win.Size)
	}
	if win.Y-margin < 0 || win.Y+win.Size+margin > 4096 {
		t.Errorf("window %+v with margin %d does not fit the image", win, margin)
	}
}

func TestLocaliseWindowRejectsCorner(t *testing.T) {
	if _, _, _, err := localiseWindow(2, 2, 4096, 4096, 30, 24); err == nil {
		t.Fatal("localiseWindow accepted a corner that cannot hold a background ring")
	}
}

func TestWriteErasedFrameIsIncrementalReadable(t *testing.T) {
	const w, h = 16, 16
	template := filepath.Join(t.TempDir(), "template.xisf")
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = float32(i) / float32(w*h)
	}
	writeFloatXISF(t, template, w, h, plane)

	out := filepath.Join(t.TempDir(), "erased.xisf")
	if err := writeErasedFrame(out, template, plane); err != nil {
		t.Fatalf("writeErasedFrame: %v", err)
	}

	f, err := xisf.Open(out)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	header := string(f.HeaderXML())
	imageTag := regexp.MustCompile(`(?s)<Image[^>]*>`).FindString(header)
	if strings.Contains(imageTag, "compression=") {
		t.Errorf("output image is compressed: %s", imageTag)
	}
	prop := regexp.MustCompile(`XISF:BlockAlignmentSize"[^>]*value="(\d+)"`).FindStringSubmatch(header)
	if prop == nil || prop[1] != "4096" {
		t.Errorf("XISF:BlockAlignmentSize = %v, want 4096", prop)
	}
	loc := regexp.MustCompile(`location="attachment:(\d+):`).FindStringSubmatch(imageTag)
	if loc == nil {
		t.Fatalf("no attachment location: %s", imageTag)
	}
	pos, err := strconv.Atoi(loc[1])
	if err != nil {
		t.Fatal(err)
	}
	if pos%4096 != 0 {
		t.Errorf("attachment starts at %d, want a 4096-byte aligned offset", pos)
	}

	got, err := xisf.Plane[float32](f.Images()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range plane {
		if got[i] != plane[i] {
			t.Fatalf("pixel %d = %v, want %v", i, got[i], plane[i])
		}
	}
}

func TestRunEraseDrizzleSource(t *testing.T) {
	const w, h = 256, 256
	const cx, cy = 128, 128

	root := t.TempDir()
	products := filepath.Join(root, "products")
	input := filepath.Join(root, "registered", "Light_FILTER-L_mono")
	sourceDir := filepath.Join(root, "calibrated", "Light_FILTER-L_mono")
	for _, dir := range []string{input, sourceDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	catalogue := []report.Artefact{{N: 1, X: cx, Y: cy, SNR: 10, Scale: 8}}
	for _, filter := range []string{"L", "R"} {
		writeUnionCatalogue(t, products, filter, catalogue)
	}
	writeUnionCatalogue(t, products, "LR", catalogue)
	writeUnionBackground(t, products, "LR", report.Background{
		Filter: "L", X: 20, Y: 20, Size: 21,
		Mean: 0.01, Noise: 0.001, SNR: 10, Pixels: 441,
	})

	clean := eraseTestSky(w, h, 0.01, 0.001, 1)
	writeFloatXISF(t, filepath.Join(products, "L", stackFileName(stackCleanKind, "L")), w, h, clean)

	registered := eraseTargetFromClean(clean, 0.01, 10)
	addEraseGhost(registered, w, cx, cy, 0.004, 4)
	writeFloatXISF(t, filepath.Join(input, "a.xisf"), w, h, registered)

	const sourceName = "a_c.xisf"
	sourcePath := filepath.Join(sourceDir, sourceName)
	writeFloatXISF(t, sourcePath, w, h, registered)

	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	xdrzText := `<?xml version="1.0" encoding="UTF-8"?>
<xdrz version="1.1" xmlns="http://www.pixinsight.com/xdrz">
<SourceImage>` + absSource + `</SourceImage>
<AlignmentTargetImage>/registered/a.xisf</AlignmentTargetImage>
<ReferenceGeometry width="256" height="256" numberOfChannels="1"/>
<AlignmentOrigin x="0.5" y="0.5"/>
<AlignmentMatrix>1,0,0,0,1,0,0,0,1</AlignmentMatrix>
</xdrz>`
	xdrzPath := filepath.Join(input, "a.xdrz")
	if err := os.WriteFile(xdrzPath, []byte(xdrzText), 0o644); err != nil {
		t.Fatal(err)
	}

	// A clean registered frame and its source must be copied, with its own
	// sidecar updated, even though no inpainting happens.
	cleanRegistered := eraseTargetFromClean(clean, 0.01, 11)
	writeFloatXISF(t, filepath.Join(input, "b.xisf"), w, h, cleanRegistered)
	cleanSourceName := "b_c.xisf"
	cleanSourcePath := filepath.Join(sourceDir, cleanSourceName)
	writeFloatXISF(t, cleanSourcePath, w, h, cleanRegistered)
	absCleanSource, err := filepath.Abs(cleanSourcePath)
	if err != nil {
		t.Fatal(err)
	}
	cleanXDRZ := strings.ReplaceAll(xdrzText, absSource, absCleanSource)
	if err := os.WriteFile(filepath.Join(input, "b.xdrz"), []byte(cleanXDRZ), 0o644); err != nil {
		t.Fatal(err)
	}

	writeDetectionReport(t, filepath.Join(products, "L", detectionName), []report.Row{
		{File: "a.xisf", Detected: true, Width: w, Height: h, PeakSNR: 20},
		{File: "b.xisf", Detected: false, Width: w, Height: h, PeakSNR: 2},
	})

	opts := &eraseOptions{
		input:        input,
		output:       filepath.Join(root, "out"),
		products:     products,
		radius:       8,
		feather:      2,
		searchRadius: 24,
		workers:      1,
		drizzle:      true,
	}
	if err := runErase(context.Background(), []string{input}, opts); err != nil {
		t.Fatalf("runErase: %v", err)
	}

	cleanSource := filepath.Join(products, "calibrated", "L", sourceName)
	plane, sw, sh, err := loadPlane(cleanSource)
	if err != nil {
		t.Fatalf("load cleaned source: %v", err)
	}
	if sw != w || sh != h {
		t.Fatalf("cleaned source is %d×%d, want %dx%d", sw, sh, w, h)
	}
	if got := plane[cy*w+cx]; got > 0.03 {
		t.Errorf("cleaned source artefact centre = %v, want the ghost removed", got)
	}

	copyXDRZ := filepath.Join(root, "out", "L", "a.xdrz")
	raw, err := os.ReadFile(copyXDRZ)
	if err != nil {
		t.Fatalf("read copied xdrz: %v", err)
	}
	absClean, err := filepath.Abs(cleanSource)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<SourceImage>"+absClean+"</SourceImage>") {
		t.Errorf("copied xdrz SourceImage does not point at %s", absClean)
	}
	if _, err := os.Stat(filepath.Join(root, "out", "L", "a.xisf")); err != nil {
		t.Errorf("registered output missing: %v", err)
	}

	cleanCopy := filepath.Join(products, "calibrated", "L", cleanSourceName)
	if _, err := os.Stat(cleanCopy); err != nil {
		t.Errorf("clean source copy missing: %v", err)
	}
	cleanCopyXDRZ := filepath.Join(root, "out", "L", "b.xdrz")
	raw, err = os.ReadFile(cleanCopyXDRZ)
	if err != nil {
		t.Fatalf("read clean copied xdrz: %v", err)
	}
	absCleanCopy, err := filepath.Abs(cleanCopy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<SourceImage>"+absCleanCopy+"</SourceImage>") {
		t.Errorf("clean copied xdrz does not point at %s", absCleanCopy)
	}
}

func TestRunEraseDrizzleInplace(t *testing.T) {
	const w, h = 256, 256
	const cx, cy = 128, 128

	root := t.TempDir()
	products := filepath.Join(root, "products")
	input := filepath.Join(root, "registered", "Light_FILTER-L_mono")
	sourceDir := filepath.Join(root, "calibrated", "Light_FILTER-L_mono")
	for _, dir := range []string{input, sourceDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	catalogue := []report.Artefact{{N: 1, X: cx, Y: cy, SNR: 10, Scale: 8}}
	for _, filter := range []string{"L", "R"} {
		writeUnionCatalogue(t, products, filter, catalogue)
	}
	writeUnionCatalogue(t, products, "LR", catalogue)
	writeUnionBackground(t, products, "LR", report.Background{
		Filter: "L", X: 20, Y: 20, Size: 21,
		Mean: 0.01, Noise: 0.001, SNR: 10, Pixels: 441,
	})

	clean := eraseTestSky(w, h, 0.01, 0.001, 1)
	writeFloatXISF(t, filepath.Join(products, "L", stackFileName(stackCleanKind, "L")), w, h, clean)

	registered := eraseTargetFromClean(clean, 0.01, 10)
	addEraseGhost(registered, w, cx, cy, 0.004, 4)
	registeredPath := filepath.Join(input, "a.xisf")
	writeFloatXISF(t, registeredPath, w, h, registered)
	registeredBefore, err := os.ReadFile(registeredPath)
	if err != nil {
		t.Fatal(err)
	}

	sourceName := "a_c.xisf"
	sourcePath := filepath.Join(sourceDir, sourceName)
	writeFloatXISF(t, sourcePath, w, h, registered)

	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	xdrzText := `<?xml version="1.0" encoding="UTF-8"?>
<xdrz version="1.1" xmlns="http://www.pixinsight.com/xdrz">
<SourceImage>` + absSource + `</SourceImage>
<AlignmentTargetImage>/registered/a.xisf</AlignmentTargetImage>
<ReferenceGeometry width="256" height="256" numberOfChannels="1"/>
<AlignmentOrigin x="0.5" y="0.5"/>
<AlignmentMatrix>1,0,0,0,1,0,0,0,1</AlignmentMatrix>
</xdrz>`
	xdrzPath := filepath.Join(input, "a.xdrz")
	if err := os.WriteFile(xdrzPath, []byte(xdrzText), 0o644); err != nil {
		t.Fatal(err)
	}
	xdrzBefore, err := os.ReadFile(xdrzPath)
	if err != nil {
		t.Fatal(err)
	}

	writeDetectionReport(t, filepath.Join(products, "L", detectionName), []report.Row{
		{File: "a.xisf", Detected: true, Width: w, Height: h, PeakSNR: 20},
	})

	opts := &eraseOptions{
		input:        input,
		output:       filepath.Join(root, "out"),
		products:     products,
		radius:       8,
		feather:      2,
		searchRadius: 24,
		workers:      1,
		drizzle:      true,
		inplace:      true,
	}
	if err := runErase(context.Background(), []string{input}, opts); err != nil {
		t.Fatalf("runErase: %v", err)
	}

	source, sw, sh, err := loadPlane(sourcePath)
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	if sw != w || sh != h {
		t.Fatalf("source is %d×%d, want %dx%d", sw, sh, w, h)
	}
	if got := source[cy*w+cx]; got > 0.03 {
		t.Errorf("source artefact centre = %v, want the ghost removed", got)
	}

	registeredAfter, err := os.ReadFile(registeredPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(registeredBefore, registeredAfter) {
		t.Error("registered input was rewritten by --drizzle --inplace")
	}
	xdrzAfter, err := os.ReadFile(xdrzPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(xdrzBefore, xdrzAfter) {
		t.Error("xdrz sidecar was changed by --drizzle --inplace")
	}
	if _, err := os.Stat(filepath.Join(root, "out")); !os.IsNotExist(err) {
		t.Errorf("--drizzle --inplace wrote an output directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(products, "calibrated")); !os.IsNotExist(err) {
		t.Errorf("--drizzle --inplace wrote a calibrated directory: %v", err)
	}
}

func TestRunEraseInPlaceNoApplied(t *testing.T) {
	const w, h = 128, 128
	const artX, artY = 64, 64

	root := t.TempDir()
	products := filepath.Join(root, "products")
	input := filepath.Join(root, "registered", "Light_FILTER-L_mono")
	if err := os.MkdirAll(input, 0o755); err != nil {
		t.Fatal(err)
	}

	catalogue := []report.Artefact{{N: 1, X: artX, Y: artY, SNR: 10, Scale: 8}}
	for _, filter := range []string{"L", "R"} {
		writeUnionCatalogue(t, products, filter, catalogue)
	}
	writeUnionCatalogue(t, products, "LR", catalogue)
	writeUnionBackground(t, products, "LR", report.Background{
		Filter: "L", X: 20, Y: 20, Size: 21,
		Mean: 0.01, Noise: 0.001, SNR: 10, Pixels: 441,
	})
	clean := eraseTestSky(w, h, 0.01, 0.001, 1)
	writeFloatXISF(t, filepath.Join(products, "L", stackFileName(stackCleanKind, "L")), w, h, clean)

	plane := eraseTargetFromClean(clean, 0.01, 10)
	addEraseGhost(plane, w, artX, artY, 0.05, 4)
	frame := filepath.Join(input, "a.xisf")
	writeFloatXISF(t, frame, w, h, plane)
	before, err := os.ReadFile(frame)
	if err != nil {
		t.Fatal(err)
	}
	writeDetectionReport(t, filepath.Join(products, "L", detectionName), []report.Row{
		{File: "a.xisf", Detected: true, Width: w, Height: h, PeakSNR: 20},
	})

	opts := &eraseOptions{
		input:        input,
		output:       filepath.Join(root, "out"),
		products:     products,
		radius:       8,
		feather:      2,
		searchRadius: 24,
		workers:      1,
		minSNR:       1000,
		inplace:      true,
	}
	if err := runErase(context.Background(), []string{input}, opts); err != nil {
		t.Fatalf("runErase: %v", err)
	}
	after, err := os.ReadFile(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("an in-place frame with no applied artefact was rewritten")
	}
}

func TestCopyFileSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frame.xisf")
	want := []byte("not really a fits file")
	if err := os.WriteFile(path, want, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(path, path); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("copying a file onto itself changed it: %q", got)
	}
}

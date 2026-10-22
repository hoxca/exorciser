package cmd

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/xisf"
)

func TestScanRejectsBadFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "count", args: []string{"--count", "1"}},
		{name: "sigma", args: []string{"--sigma", "0"}},
		{name: "iterations", args: []string{"--clip-iterations", "0"}},
		{name: "min frames", args: []string{"--min-frames", "0"}},
		{name: "min frames above count", args: []string{"--min-frames", "20"}},
		{name: "workers", args: []string{"--workers", "0"}},
		{name: "min snr", args: []string{"--min-snr", "0"}},
		{name: "margin", args: []string{"--margin", "-1"}},
		{name: "star sigma", args: []string{"--star-sigma", "0"}},
		{name: "star dilation", args: []string{"--star-dilation", "-1"}},
		{name: "csv output", args: []string{"-o", "out.csv"}},
		{name: "scales", args: []string{"--ghost-sigma", "four"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newScanCmd()
			cmd.SetArgs(tt.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			if err := cmd.Execute(); err == nil {
				t.Fatalf("scan accepted %v, want an error", tt.args)
			}
		})
	}
}

func TestSelectScanRows(t *testing.T) {
	rows := []report.Row{
		{File: "g1.xisf", Detected: true, PeakSNR: 12},
		{File: "g2.xisf", Detected: true, PeakSNR: -3},
		{File: "g3.xisf", Detected: true, PeakSNR: 7},
		{File: "g1.xisf", Detected: true, PeakSNR: 12},
		{File: "error.xisf", Detected: true, PeakSNR: 99, Err: "broken"},
		{File: "nan.xisf", Detected: true, PeakSNR: math.NaN()},
		{File: "c1.xisf", Detected: false, PeakSNR: -5},
		{File: "c2.xisf", Detected: false, PeakSNR: 2},
		{File: "c3.xisf", Detected: false, PeakSNR: 4},
	}

	ghost, clean, err := selectScanRows(rows, 2)
	if err != nil {
		t.Fatalf("selectScanRows: %v", err)
	}
	if got, want := names(ghost), []string{"g1.xisf", "g3.xisf"}; !equalStrings(got, want) {
		t.Errorf("ghost rows = %v, want %v", got, want)
	}
	if got, want := names(clean), []string{"c1.xisf", "c3.xisf"}; !equalStrings(got, want) {
		t.Errorf("clean rows = %v, want %v", got, want)
	}

	ghost, clean, err = selectScanRows(rows, 10)
	if err != nil {
		t.Fatalf("selectScanRows: %v", err)
	}
	if len(ghost) != 3 || len(clean) != 3 {
		t.Errorf("got %d ghost and %d clean rows, want 3 and 3", len(ghost), len(clean))
	}
}

func TestSelectScanRowsNeedsBothClasses(t *testing.T) {
	rows := []report.Row{
		{File: "g1.xisf", Detected: true, PeakSNR: 12},
		{File: "g2.xisf", Detected: true, PeakSNR: 7},
		{File: "c1.xisf", Detected: false, PeakSNR: 2},
	}
	if _, _, err := selectScanRows(rows, 2); err == nil {
		t.Fatal("selectScanRows accepted a single clean frame")
	}
}

func TestFramesByName(t *testing.T) {
	byName, err := framesByName([]string{"/a/x.xisf", "/b/y.xisf"})
	if err != nil {
		t.Fatalf("framesByName: %v", err)
	}
	if byName["x.xisf"] != "/a/x.xisf" || byName["y.xisf"] != "/b/y.xisf" {
		t.Errorf("framesByName = %v", byName)
	}

	if _, err := framesByName([]string{"/a/x.xisf", "/b/x.xisf"}); err == nil {
		t.Fatal("framesByName accepted duplicate basenames")
	}
}

func TestResolveScanPaths(t *testing.T) {
	byName := map[string]string{"a.xisf": "/dir/a.xisf"}
	paths, err := resolveScanPaths([]report.Row{
		{File: "a.xisf"},
		{File: "/elsewhere/a.xisf"},
	}, byName)
	if err != nil {
		t.Fatalf("resolveScanPaths: %v", err)
	}
	if len(paths) != 2 || paths[0] != "/dir/a.xisf" || paths[1] != "/dir/a.xisf" {
		t.Errorf("paths = %v", paths)
	}

	if _, err := resolveScanPaths([]report.Row{{File: "missing.xisf"}}, byName); err == nil {
		t.Fatal("resolveScanPaths accepted a missing frame")
	}
}

func TestWriteStack(t *testing.T) {
	dir := t.TempDir()
	template := filepath.Join(dir, "template.xisf")
	output := filepath.Join(dir, "stack.xisf")

	const w, h = 16, 12
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = float32(i) / 100
	}
	writeFloatXISF(t, template, w, h, plane)

	if err := writeStack(output, template, plane, w, h, "stack_ghost"); err != nil {
		t.Fatalf("writeStack: %v", err)
	}

	f, err := xisf.Open(output)
	if err != nil {
		t.Fatalf("open stack: %v", err)
	}
	images := f.Images()
	if len(images) != 1 {
		t.Fatalf("stack has %d images, want 1", len(images))
	}
	im := images[0]
	if im.Width() != w || im.Height() != h {
		t.Errorf("stack is %d×%d, want %d×%d", im.Width(), im.Height(), w, h)
	}
	if im.SampleFormat != xisf.Float32 {
		t.Errorf("sample format = %v, want Float32", im.SampleFormat)
	}
	if !im.HasBounds {
		t.Fatal("stack lost bounds, PixInsight rejects float images without them")
	}
	if im.Bounds != [2]float64{0, 1} {
		t.Errorf("bounds = %v, want [0 1]", im.Bounds)
	}
	if low, high, ok := im.RepresentableRange(); !ok || low != 0 || high != 1 {
		t.Errorf("representable range = %v:%v (ok=%v), want 0:1", low, high, ok)
	}
	if im.ID != "stack_ghost" {
		t.Errorf("id = %q, want stack_ghost", im.ID)
	}
	if im.ImageType != "MasterLight" {
		t.Errorf("image type = %q, want MasterLight", im.ImageType)
	}

	got, err := plane32(im)
	if err != nil {
		t.Fatalf("read stack pixels: %v", err)
	}
	if len(got) != len(plane) {
		t.Fatalf("stack has %d pixels, want %d", len(got), len(plane))
	}
	for i := range plane {
		if got[i] != plane[i] {
			t.Fatalf("pixel %d = %v, want %v", i, got[i], plane[i])
		}
	}

	// A float template without bounds still yields a valid float master.
	bareTemplate := filepath.Join(dir, "template-bare.xisf")
	bareOutput := filepath.Join(dir, "stack-bare.xisf")
	writeFloatXISFWithoutBounds(t, bareTemplate, w, h, plane)
	if err := writeStack(bareOutput, bareTemplate, plane, w, h, "stack_ghost"); err != nil {
		t.Fatalf("writeStack without template bounds: %v", err)
	}
	bare, err := xisf.Open(bareOutput)
	if err != nil {
		t.Fatalf("open stack without template bounds: %v", err)
	}
	if len(bare.Images()) != 1 {
		t.Fatalf("bare stack has %d images, want 1", len(bare.Images()))
	}
	bareImage := bare.Images()[0]
	if low, high, ok := bareImage.RepresentableRange(); !ok || low != 0 || high != 1 {
		t.Errorf("bare representable range = %v:%v (ok=%v), want 0:1", low, high, ok)
	}
}

const (
	scanW = 256
	scanH = 256

	scanStarX = 80
	scanStarY = 80

	scanGhostX = 170
	scanGhostY = 140
)

func TestRunScanEndToEnd(t *testing.T) {
	dir := t.TempDir()
	frames := filepath.Join(dir, "frames")
	if err := os.Mkdir(frames, 0o755); err != nil {
		t.Fatal(err)
	}

	var rows []report.Row
	for i := 0; i < 4; i++ {
		clean := filepath.Join(frames, fmt.Sprintf("clean_%02d.xisf", i))
		writeFloatXISF(t, clean, scanW, scanH, makeScanFrame(int64(i+1), 0.5, 0))
		rows = append(rows, report.Row{
			File: filepath.Base(clean), Detected: false,
			Width: scanW, Height: scanH, PeakSNR: 2,
		})

		ghost := filepath.Join(frames, fmt.Sprintf("ghost_%02d.xisf", i))
		// The star is 10% brighter in the detected group, which leaves a
		// large residual at its centre unless the star mask removes it.
		writeFloatXISF(t, ghost, scanW, scanH, makeScanFrame(int64(100+i), 0.55, 0.012))
		rows = append(rows, report.Row{
			File: filepath.Base(ghost), Detected: true,
			Width: scanW, Height: scanH, PeakSNR: 10,
		})
	}

	reportPath := filepath.Join(dir, "detection.csv")
	rf, err := os.Create(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := report.Write(rf, rows); err != nil {
		t.Fatal(err)
	}
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(dir, "out")
	opts := &scanOptions{
		report:       reportPath,
		outputDir:    outDir,
		count:        4,
		sigma:        3,
		iterations:   3,
		minFrames:    2,
		workers:      2,
		minSNR:       6,
		scales:       []float64{4, 8, 16},
		margin:       16,
		starSigma:    50,
		starDilation: 4,
	}
	if err := runScan(context.Background(), []string{frames}, opts); err != nil {
		t.Fatalf("runScan: %v", err)
	}

	artefacts := readScanArtefacts(t, filepath.Join(outDir, artefactsName))
	if len(artefacts) == 0 {
		t.Fatal("scan wrote no artefacts")
	}
	if artefacts[0].N != 1 {
		t.Errorf("first artefact is number %d, want 1", artefacts[0].N)
	}
	for i := 1; i < len(artefacts); i++ {
		if math.Abs(artefacts[i].SNR) > math.Abs(artefacts[i-1].SNR) {
			t.Fatalf("artefacts are not sorted by |snr| at row %d", i)
		}
		if artefacts[i].N != artefacts[i-1].N+1 {
			t.Fatalf("artefact numbering jumps from %d to %d", artefacts[i-1].N, artefacts[i].N)
		}
	}

	var found bool
	for _, a := range artefacts {
		d := math.Hypot(float64(a.X-scanGhostX), float64(a.Y-scanGhostY))
		if d < 8 {
			found = true
			if math.Abs(a.SNR) < 6 {
				t.Errorf("ghost detection snr = %v, want at least 6", a.SNR)
			}
		}
		if math.Hypot(float64(a.X-scanStarX), float64(a.Y-scanStarY)) < 8 {
			t.Errorf("star leaked into the catalogue: %+v", a)
		}
	}
	if !found {
		t.Errorf("no artefact within 8 px of the injected ghost (%d,%d): %v", scanGhostX, scanGhostY, artefacts)
	}

	ghostPlane, gw, gh, err := loadPlane(filepath.Join(outDir, stackGhostName))
	if err != nil {
		t.Fatalf("load ghost master: %v", err)
	}
	cleanPlane, cw, ch, err := loadPlane(filepath.Join(outDir, stackCleanName))
	if err != nil {
		t.Fatalf("load clean master: %v", err)
	}
	if gw != scanW || gh != scanH || cw != scanW || ch != scanH {
		t.Fatalf("masters are %d×%d and %d×%d, want %d×%d", gw, gh, cw, ch, scanW, scanH)
	}
	// Fit the known Gaussian to the difference of the masters. A single pixel
	// carries the full stack noise, so use the shape of the injected ghost.
	var num, den float64
	for y := scanGhostY - 24; y <= scanGhostY+24; y++ {
		for x := scanGhostX - 24; x <= scanGhostX+24; x++ {
			g := gaussianAt(x, y, scanGhostX, scanGhostY, 6)
			num += g * float64(ghostPlane[y*scanW+x]-cleanPlane[y*scanW+x])
			den += g * g
		}
	}
	if got := num / den; math.Abs(got-0.012) > 0.002 {
		t.Errorf("fitted ghost amplitude = %v, want about 0.012", got)
	}
}

func makeScanFrame(seed int64, starAmp, ghostAmp float64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	plane := make([]float32, scanW*scanH)
	for y := 0; y < scanH; y++ {
		for x := 0; x < scanW; x++ {
			v := 0.5 + 0.0002*float64(x) + 0.0001*float64(y)
			v += rng.NormFloat64() * 0.005
			v += starAmp * gaussianAt(x, y, scanStarX, scanStarY, 1.5)
			if ghostAmp != 0 {
				v += ghostAmp * gaussianAt(x, y, scanGhostX, scanGhostY, 6)
			}
			plane[y*scanW+x] = float32(v)
		}
	}
	return plane
}

func gaussianAt(x, y, cx, cy int, sigma float64) float64 {
	dx := float64(x - cx)
	dy := float64(y - cy)
	return math.Exp(-(dx*dx + dy*dy) / (2 * sigma * sigma))
}

// writeFloatXISF writes a minimal uncompressed Float32 XISF image with bounds.
func writeFloatXISF(t *testing.T, path string, w, h int, pixels []float32) {
	t.Helper()
	writeFloatXISFWithBounds(t, path, w, h, pixels, ` bounds="0:1"`)
}

// writeFloatXISFWithoutBounds writes a Float32 XISF image with no bounds
// attribute, as some integer-era templates do.
func writeFloatXISFWithoutBounds(t *testing.T, path string, w, h int, pixels []float32) {
	t.Helper()
	writeFloatXISFWithBounds(t, path, w, h, pixels, "")
}

func writeFloatXISFWithBounds(t *testing.T, path string, w, h int, pixels []float32, bounds string) {
	t.Helper()
	if len(pixels) != w*h {
		t.Fatalf("writeFloatXISF: %d pixels for %d×%d", len(pixels), w, h)
	}
	size := len(pixels) * 4
	img := fmt.Sprintf(`<Image id="test" geometry="%d:%d:1" sampleFormat="Float32"%s colorSpace="Gray"`, w, h, bounds)
	meta := `<Metadata>` +
		`<Property id="XISF:CreationTime" type="String">2026-01-01T00:00:00Z</Property>` +
		`<Property id="XISF:CreatorApplication" type="String">exorciser-test</Property>` +
		`</Metadata>`
	makeHeader := func(position, size int) string {
		return `<?xml version="1.0" encoding="UTF-8"?>` +
			`<xisf version="1.0" xmlns="http://www.pixinsight.com/xisf">` +
			img + fmt.Sprintf(` location="attachment:%d:%d"/>`, position, size) +
			meta + `</xisf>`
	}

	header := makeHeader(0, size)
	position := int64(xisf.PreambleLen + len(header))
	for i := 0; i < 32; i++ {
		next := makeHeader(int(position), size)
		nextPosition := int64(xisf.PreambleLen + len(next))
		header = next
		if nextPosition == position {
			break
		}
		position = nextPosition
	}

	out := make([]byte, 0, int(position)+size)
	out = append(out, []byte(xisf.Signature)...)
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(header)))
	out = append(out, n[:]...)
	out = append(out, 0, 0, 0, 0)
	out = append(out, header...)
	for int64(len(out)) < position {
		out = append(out, 0)
	}
	for _, v := range pixels {
		binary.LittleEndian.PutUint32(n[:], math.Float32bits(v))
		out = append(out, n[:]...)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

type scanArtefact struct {
	N   int
	X   int
	Y   int
	SNR float64
}

func readScanArtefacts(t *testing.T, path string) []scanArtefact {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open artefacts: %v", err)
	}
	defer func() { _ = f.Close() }()

	reader := csv.NewReader(f)
	reader.Comma = report.Separator
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read artefacts: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("artefacts file is empty")
	}
	if !equalStrings(records[0], report.ArtefactHeader) {
		t.Fatalf("header = %v, want %v", records[0], report.ArtefactHeader)
	}

	var out []scanArtefact
	for i, rec := range records[1:] {
		if len(rec) < 4 {
			t.Fatalf("row %d has %d fields", i+2, len(rec))
		}
		var a scanArtefact
		if _, err := fmt.Sscanf(rec[0], "%d", &a.N); err != nil {
			t.Fatalf("row %d n: %v", i+2, err)
		}
		if _, err := fmt.Sscanf(rec[1], "%d", &a.X); err != nil {
			t.Fatalf("row %d x: %v", i+2, err)
		}
		if _, err := fmt.Sscanf(rec[2], "%d", &a.Y); err != nil {
			t.Fatalf("row %d y: %v", i+2, err)
		}
		snr := rec[3]
		for j := range snr {
			if snr[j] == ',' {
				snr = snr[:j] + "." + snr[j+1:]
				break
			}
		}
		if _, err := fmt.Sscanf(snr, "%f", &a.SNR); err != nil {
			t.Fatalf("row %d snr: %v", i+2, err)
		}
		out = append(out, a)
	}
	return out
}

func names(rows []report.Row) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.File
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

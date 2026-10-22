package cmd

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hoxca/exorciser/internal/detect"
	"github.com/hoxca/exorciser/internal/report"
)

func TestParseScales(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []float64
		wantErr bool
	}{
		{name: "defaults", in: "4,8,16", want: []float64{4, 8, 16}},
		{name: "spaces", in: " 4 , 8 ", want: []float64{4, 8}},
		{name: "single", in: "8", want: []float64{8}},
		{name: "fraction", in: "6.5", want: []float64{6.5}},
		{name: "empty", in: "", wantErr: true},
		{name: "only commas", in: ",,", wantErr: true},
		{name: "letters", in: "four", wantErr: true},
		{name: "zero", in: "0", wantErr: true},
		{name: "negative", in: "-4", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseScales(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseScales(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseScales(%q): %v", tt.in, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseScales(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("parseScales(%q) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

func TestExcludeReference(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.xisf")
	other := filepath.Join(dir, "other.xisf")
	paths := []string{ref, other}

	kept, skipped := excludeReference(paths, ref)
	if !skipped {
		t.Error("excludeReference did not skip the reference")
	}
	if len(kept) != 1 || kept[0] != other {
		t.Errorf("kept = %v, want only %q", kept, other)
	}

	// A relative path must match the same file as an absolute one.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, ref)
	if err != nil {
		t.Fatal(err)
	}
	kept, skipped = excludeReference(paths, rel)
	if !skipped || len(kept) != 1 {
		t.Errorf("relative reference: kept = %v, skipped = %v", kept, skipped)
	}

	kept, skipped = excludeReference(paths, filepath.Join(dir, "absent.xisf"))
	if skipped || len(kept) != 2 {
		t.Errorf("absent reference: kept = %v, skipped = %v", kept, skipped)
	}
}

func TestDetectRequiresReference(t *testing.T) {
	cmd := newDetectCmd()
	cmd.SetArgs([]string{"-o", t.TempDir()})
	if err := cmd.Execute(); err == nil {
		t.Fatal("detect ran without --reference, want an error")
	}
}

func TestDetectRejectsCSVOutputPath(t *testing.T) {
	cmd := newDetectCmd()
	cmd.SetArgs([]string{"--reference", "ref.xisf", "-o", "out.csv"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("detect accepted a .csv path for --output, want an error")
	}
}

func TestDetectRejectsSmallMargin(t *testing.T) {
	cmd := newDetectCmd()
	cmd.SetArgs([]string{"--reference", "ref.xisf", "--margin", "10"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("detect accepted a margin smaller than the kernel radius, want an error")
	}
}

func TestDetectRejectsBadMinSNR(t *testing.T) {
	for _, value := range []string{"0", "-6", "NaN", "Inf"} {
		t.Run(value, func(t *testing.T) {
			cmd := newDetectCmd()
			cmd.SetArgs([]string{"--reference", "ref.xisf", "--min-snr=" + value})
			if err := cmd.Execute(); err == nil {
				t.Fatalf("detect accepted --min-snr %s, want an error", value)
			}
		})
	}
}

func TestParseCoord(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    *point
		wantErr bool
	}{
		{name: "empty means auto", in: ""},
		{name: "blank means auto", in: "   "},
		{name: "plain", in: "2454,1990", want: &point{x: 2454, y: 1990}},
		{name: "surrounding spaces", in: " 2454 , 1990 ", want: &point{x: 2454, y: 1990}},
		{name: "origin", in: "0,0", want: &point{}},
		{name: "one value", in: "2454", wantErr: true},
		{name: "three values", in: "2454,1990,10", wantErr: true},
		{name: "negative", in: "-1,5", wantErr: true},
		{name: "float", in: "1.5,2", wantErr: true},
		{name: "letters", in: "a,b", wantErr: true},
		{name: "trailing comma", in: "2454,", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCoord(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseCoord(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCoord(%q): %v", tt.in, err)
			}
			if tt.want == nil {
				if got != nil {
					t.Fatalf("parseCoord(%q) = %+v, want nil", tt.in, got)
				}
				return
			}
			if got == nil || *got != *tt.want {
				t.Fatalf("parseCoord(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestWindowFor(t *testing.T) {
	win, err := windowFor(nil, 128, 4096, 4096)
	if err != nil {
		t.Fatalf("windowFor: %v", err)
	}
	if want := (detect.Window{X: 1984, Y: 1984, Size: 128}); win != want {
		t.Fatalf("default window = %+v, want %+v", win, want)
	}

	win, err = windowFor(&point{x: 2454, y: 1990}, 128, 4096, 4096)
	if err != nil {
		t.Fatalf("windowFor: %v", err)
	}
	if want := (detect.Window{X: 2390, Y: 1926, Size: 128}); win != want {
		t.Fatalf("coord window = %+v, want %+v", win, want)
	}

	// A window crossing the frame edge is rejected, not shifted.
	if _, err := windowFor(&point{x: 10, y: 10}, 128, 4096, 4096); err == nil {
		t.Fatal("windowFor accepted a window outside the image")
	}
}

func TestRunDetectOnSampleFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("decodes two 4096×4096 sample frames")
	}

	input := filepath.Join("..", defaultInput)
	paths, err := collect([]string{input})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(paths) < 2 {
		t.Skipf("need two sample files in %s, found %d", input, len(paths))
	}

	// The known ghost-free frame is the reference; the other frame is measured.
	reference := paths[0]
	for _, p := range paths {
		if filepath.Base(p) == referenceName {
			reference = p
			break
		}
	}

	outputDir := t.TempDir()
	opts := &detectOptions{
		input:     input,
		outputDir: outputDir,
		reference: reference,
		window:    128,
		workers:   1,
		margin:    64,
		scales:    []float64{4, 8, 16},
		minSNR:    6,
		coord:     &point{x: ghostX, y: ghostY},
	}
	// The reference is among the paths and must be skipped.
	analysed := ""
	for _, p := range paths[:2] {
		if p != reference {
			analysed = p
		}
	}
	if analysed == "" {
		t.Skipf("reference %q is not among the first two sample files", filepath.Base(reference))
	}
	if err := runDetect(context.Background(), paths[:2], opts); err != nil {
		t.Fatalf("runDetect: %v", err)
	}

	records := readDetectionReport(t, outputDir)
	if len(records) != 2 {
		t.Fatalf("detection report has %d records, want header plus one row", len(records))
	}

	row := records[1]
	if got := row[detectionColumnIndex(t, "error")]; got != "" {
		t.Fatalf("error = %q", got)
	}
	if file := row[detectionColumnIndex(t, "file")]; file != filepath.Base(analysed) {
		t.Errorf("file = %q, want %q", file, filepath.Base(analysed))
	}
	for name, want := range map[string]string{
		"detected": "true",
		"width":    "4096",
		"height":   "4096",
		"win_x":    "2396",
		"win_y":    "1916",
		"win_size": "128",
	} {
		if got := row[detectionColumnIndex(t, name)]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	if a := parseDetection(t, row, "norm_a"); a < 0.9 || a > 1.1 {
		t.Errorf("norm_a = %v, want a slope near 1 for frames of the same night", a)
	}
	if noise := parseDetection(t, row, "noise"); noise <= 0 {
		t.Errorf("noise = %v, want positive", noise)
	}
	if scale := parseDetection(t, row, "ghost_sigma"); scale != 4 && scale != 8 && scale != 16 {
		t.Errorf("ghost_sigma = %v, want one of the requested scales", scale)
	}
	peakX := parseDetection(t, row, "peak_x")
	peakY := parseDetection(t, row, "peak_y")
	if peakX < 2396 || peakX >= 2396+128 || peakY < 1916 || peakY >= 1916+128 {
		t.Errorf("peak at (%v,%v), want inside the 128 px window at (2396,1916)", peakX, peakY)
	}
}

// referenceName is the known ghost-free frame used by the sample test and by
// manual runs.
const referenceName = "VDB149_LIGHT_L_600s_BIN1_002_-09C_20220821_235716_905_W_c_r.xisf"

// ghostX and ghostY locate the 128×128 window containing a known ghost.
const (
	ghostX = 2460
	ghostY = 1980
)

// readDetectionReport parses detection.csv inside dir.
func readDetectionReport(t *testing.T, dir string) [][]string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, detectionName))
	if err != nil {
		t.Fatalf("open detection report: %v", err)
	}
	defer func() { _ = f.Close() }()

	reader := csv.NewReader(f)
	reader.Comma = report.Separator
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read detection report: %v", err)
	}
	return records
}

// parseDetection reads a numeric detection report cell, undoing the decimal
// comma.
func parseDetection(t *testing.T, row []string, name string) float64 {
	t.Helper()
	raw := strings.Replace(row[detectionColumnIndex(t, name)], string(report.DecimalSeparator), ".", 1)
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}

func detectionColumnIndex(t *testing.T, name string) int {
	t.Helper()
	for i, h := range report.Header {
		if h == name {
			return i
		}
	}
	t.Fatalf("no %q column in detection header", name)
	return -1
}

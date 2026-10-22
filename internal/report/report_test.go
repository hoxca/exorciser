package report

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
)

func TestFieldsMatchHeader(t *testing.T) {
	r := Row{File: "a.xisf", Width: 4096, Height: 4096, WinSize: 128}
	if got, want := len(r.Fields()), len(Header); got != want {
		t.Fatalf("Fields has %d columns, header has %d", got, want)
	}
}

func TestWrite(t *testing.T) {
	rows := []Row{
		{
			File:       "a.xisf",
			Width:      4096,
			Height:     4096,
			WinX:       2390,
			WinY:       1926,
			WinSize:    128,
			NormA:      0.9987654321,
			NormB:      -0.0001234567,
			Noise:      0.004321,
			GhostSigma: 8,
			PeakX:      2454,
			PeakY:      1990,
			PeakSNR:    7.0214,
			PeakAmp:    -0.0012345,
			Clipped:    12,
			Detected:   true,
			DurationMS: 412,
		},
		{
			File: "b.xisf",
			Err:  "window of 128×128 pixels at (10,10) does not fit a 100×100 image",
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, rows); err != nil {
		t.Fatalf("Write: %v", err)
	}

	records, err := readAll(t, buf.String())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want header plus 2 rows", len(records))
	}
	for i, rec := range records {
		if len(rec) != len(Header) {
			t.Fatalf("record %d has %d fields, want %d", i, len(rec), len(Header))
		}
	}

	first := records[1]
	checks := map[string]string{
		"file":        "a.xisf",
		"detected":    "true",
		"width":       "4096",
		"win_x":       "2390",
		"win_size":    "128",
		"norm_a":      "0,9987654321",
		"norm_b":      "-0,0001234567",
		"noise":       "0,004321",
		"ghost_sigma": "8",
		"peak_x":      "2454",
		"peak_y":      "1990",
		"peak_snr":    "7,021",
		"peak_amp":    "-0,0012345",
		"clipped_px":  "12",
		"duration_ms": "412",
		"error":       "",
	}
	for name, want := range checks {
		if got := first[column(t, name)]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// Failed images keep the path and error, and leave the measurement blank.
	failed := records[2]
	if got := failed[column(t, "error")]; got == "" {
		t.Error("error is empty on a failed row")
	}
	for _, name := range []string{"detected", "norm_a", "noise", "ghost_sigma", "peak_x", "peak_snr", "peak_amp", "clipped_px"} {
		if got := failed[column(t, name)]; got != "" {
			t.Errorf("%s = %q on a failed row, want empty", name, got)
		}
	}
}

func TestWriteKeepsZeroValues(t *testing.T) {
	// A successful measurement that happens to be zero must say 0, not look
	// like a failed row.
	r := Row{File: "a.xisf", Width: 128, Height: 128, WinSize: 128}
	fields := r.Fields()
	for _, name := range []string{"peak_x", "peak_y", "peak_snr", "clipped_px"} {
		if got := fields[column(t, name)]; got != "0" && got != "0,000" {
			t.Errorf("%s = %q, want a zero value", name, got)
		}
	}
	if got := fields[column(t, "detected")]; got != "false" {
		t.Errorf("detected = %q, want false", got)
	}
}

func TestWriteQuotesSeparators(t *testing.T) {
	rows := []Row{{File: `odd;name "quoted".xisf`, Width: 10, Height: 10, WinSize: 4}}

	var buf bytes.Buffer
	if err := Write(&buf, rows); err != nil {
		t.Fatalf("Write: %v", err)
	}
	records, err := readAll(t, buf.String())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want header plus one row", len(records))
	}
	if got := records[1][column(t, "file")]; got != rows[0].File {
		t.Errorf("file = %q, want %q", got, rows[0].File)
	}
}

func TestWriteEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	records, err := readAll(t, buf.String())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want the header alone", len(records))
	}
	if len(records[0]) != len(Header) {
		t.Fatalf("header has %d fields, want %d", len(records[0]), len(Header))
	}
}

// readAll parses a written report back into records.
func readAll(t *testing.T, text string) ([][]string, error) {
	t.Helper()
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = Separator
	return reader.ReadAll()
}

func column(t *testing.T, name string) int {
	t.Helper()
	for i, h := range Header {
		if h == name {
			return i
		}
	}
	t.Fatalf("no %q column in header", name)
	return -1
}

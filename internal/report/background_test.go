package report_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
)

func TestWriteBackground(t *testing.T) {
	var buf bytes.Buffer
	err := report.WriteBackground(&buf, report.Background{
		Filter: "L",
		X:      980,
		Y:      3120,
		Size:   65,
		Mean:   0.003451,
		Noise:  0.000424,
		SNR:    8.138,
		Pixels: 3912,
	})
	if err != nil {
		t.Fatalf("WriteBackground: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), buf.String())
	}
	if lines[0] != strings.Join(report.BackgroundHeader, ";") {
		t.Errorf("header = %q, want %q", lines[0], strings.Join(report.BackgroundHeader, ";"))
	}
	want := "L;980;3120;65;0,003451;0,000424;8,138;3912"
	if lines[1] != want {
		t.Errorf("row = %q, want %q", lines[1], want)
	}
}

func TestReadBackground(t *testing.T) {
	const text = "filter;x;y;size;mean;noise;snr;pixels\nL;3456;1056;65;0,00946561075;0,0001119915;84,521;3769\n"

	got, err := report.ReadBackground(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ReadBackground: %v", err)
	}
	want := report.Background{
		Filter: "L",
		X:      3456,
		Y:      1056,
		Size:   65,
		Mean:   0.00946561075,
		Noise:  0.0001119915,
		SNR:    84.521,
		Pixels: 3769,
	}
	if got != want {
		t.Errorf("ReadBackground = %+v, want %+v", got, want)
	}
}

func TestReadBackgroundRequiresPosition(t *testing.T) {
	const text = "filter;mean;noise\nL;0,01;0,001\n"
	if _, err := report.ReadBackground(strings.NewReader(text)); err == nil {
		t.Fatal("ReadBackground accepted a table without x, y or size")
	}
}

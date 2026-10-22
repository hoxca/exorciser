package report_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
)

func TestReadRoundTrip(t *testing.T) {
	rows := []report.Row{
		{
			File:       "VDB149_LIGHT_035.xisf",
			Detected:   true,
			Width:      4096,
			Height:     4096,
			WinX:       2396,
			WinY:       1916,
			WinSize:    128,
			NormA:      1.0125,
			NormB:      -0.000123,
			Noise:      0.000456,
			GhostSigma: 8,
			PeakX:      2463,
			PeakY:      1982,
			PeakSNR:    17.77,
			PeakAmp:    0.000367,
			Clipped:    812,
			DurationMS: 421,
		},
		{
			File:       "VDB149_LIGHT_061.xisf",
			Detected:   false,
			Width:      4096,
			Height:     4096,
			WinX:       2396,
			WinY:       1916,
			WinSize:    128,
			NormA:      0.998,
			NormB:      0.00002,
			Noise:      0.00044,
			GhostSigma: 4,
			PeakX:      2492,
			PeakY:      2016,
			PeakSNR:    3.206,
			PeakAmp:    0.000041,
			Clipped:    640,
			DurationMS: 399,
		},
		{File: "broken.xisf", Err: "not an XISF file"},
	}

	var buf bytes.Buffer
	if err := report.Write(&buf, rows); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := report.Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != len(rows) {
		t.Fatalf("Read returned %d rows, want %d", len(got), len(rows))
	}
	for i := range rows {
		if got[i] != rows[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], rows[i])
		}
	}
}

func TestReadShuffledColumnsAndDecimalComma(t *testing.T) {
	const table = `peak_snr;file;detected;error;ignored
6,500;frame.xisf;true;;whatever
-3,250;clean.xisf;false;;
;broken.xisf;;file is truncated;
`
	rows, err := report.Read(strings.NewReader(table))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("Read returned %d rows, want 3", len(rows))
	}
	if rows[0].PeakSNR != 6.5 || !rows[0].Detected || rows[0].File != "frame.xisf" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].PeakSNR != -3.25 || rows[1].Detected {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if rows[2].Err != "file is truncated" || rows[2].PeakSNR != 0 {
		t.Errorf("row 2 = %+v", rows[2])
	}
}

func TestReadRequiresDetectionColumns(t *testing.T) {
	_, err := report.Read(strings.NewReader("file;peak_snr\na.xisf;1\n"))
	if err == nil {
		t.Fatal("Read accepted a report without a detected column")
	}
}

func TestReadRejectsBadNumber(t *testing.T) {
	_, err := report.Read(strings.NewReader("file;detected;peak_snr\na.xisf;true;many\n"))
	if err == nil {
		t.Fatal("Read accepted a non-numeric peak_snr")
	}
}

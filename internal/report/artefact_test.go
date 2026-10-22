package report_test

import (
	"bytes"
	"encoding/csv"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
)

func TestWriteArtefacts(t *testing.T) {
	var buf bytes.Buffer
	err := report.WriteArtefacts(&buf, []report.Artefact{
		{N: 1, X: 2460, Y: 1981, SNR: 42.1374, Amp: 0.0003125, Scale: 8},
		{N: 2, X: 120, Y: 3000, SNR: -7.5, Amp: -0.00002, Scale: 16},
	})
	if err != nil {
		t.Fatalf("WriteArtefacts: %v", err)
	}

	reader := csv.NewReader(&buf)
	reader.Comma = report.Separator
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	if got, want := records[0], report.ArtefactHeader; !equal(got, want) {
		t.Errorf("header = %v, want %v", got, want)
	}
	want := []string{"1", "2460", "1981", "42,137", "0,0003125", "8"}
	if !equal(records[1], want) {
		t.Errorf("row 1 = %v, want %v", records[1], want)
	}
	if got := records[2][3]; got != "-7,500" {
		t.Errorf("negative snr = %q, want -7,500", got)
	}
}

func equal(a, b []string) bool {
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

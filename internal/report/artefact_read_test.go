package report_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
)

func TestReadArtefactsRoundTrip(t *testing.T) {
	want := []report.Artefact{
		{N: 1, X: 2462, Y: 1986, SNR: 79.375, Amp: 0.0003125, Scale: 8},
		{N: 2, X: 120, Y: 3000, SNR: -7.5, Amp: -0.00002, Scale: 16},
	}

	var buf bytes.Buffer
	if err := report.WriteArtefacts(&buf, want); err != nil {
		t.Fatalf("WriteArtefacts: %v", err)
	}
	got, err := report.ReadArtefacts(&buf)
	if err != nil {
		t.Fatalf("ReadArtefacts: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d artefacts, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("artefact %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadArtefactsShuffledColumns(t *testing.T) {
	const table = `y;x;n;ignored
1986;2462;1;whatever
20;30;2;
`
	got, err := report.ReadArtefacts(strings.NewReader(table))
	if err != nil {
		t.Fatalf("ReadArtefacts: %v", err)
	}
	want := []report.Artefact{
		{N: 1, X: 2462, Y: 1986},
		{N: 2, X: 30, Y: 20},
	}
	for i := range want {
		if got[i].N != want[i].N || got[i].X != want[i].X || got[i].Y != want[i].Y {
			t.Errorf("artefact %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

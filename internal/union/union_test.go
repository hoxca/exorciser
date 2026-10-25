package union_test

import (
	"math"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/exorciser/internal/union"
)

func TestMergeAveragesJitteredPositions(t *testing.T) {
	filters := []union.Filter{
		{Name: "L", Artefacts: []report.Artefact{
			{X: 100, Y: 200, SNR: 30, Amp: 0.0003, Scale: 8},
			{X: 400, Y: 100, SNR: 9, Amp: 0.0001, Scale: 8},
		}},
		{Name: "R", Artefacts: []report.Artefact{
			{X: 102, Y: 201, SNR: 20, Amp: 0.0002, Scale: 8},
			{X: 700, Y: 700, SNR: 5, Amp: 0.00005, Scale: 16},
		}},
		{Name: "G", Artefacts: []report.Artefact{
			{X: 101, Y: 198, SNR: 10, Amp: 0.0001, Scale: 8},
		}},
	}

	merged := union.Merge(filters, 16)
	if len(merged) != 3 {
		t.Fatalf("merged %d artefacts, want 3: %+v", len(merged), merged)
	}

	// The strongest observation of the shared artefact is in L, but all three
	// filters contribute to the mean.
	first := merged[0]
	if first.N != 1 {
		t.Errorf("first artefact has n = %d, want 1", first.N)
	}
	if first.X != 101 || first.Y != 200 {
		t.Errorf("merged position = (%d,%d), want (101,200) from mean 101,199.67", first.X, first.Y)
	}
	wantSNR := (30 + 20 + 10) / 3.0
	if math.Abs(first.SNR-wantSNR) > 1e-9 {
		t.Errorf("merged snr = %v, want %v", first.SNR, wantSNR)
	}
	wantAmp := (0.0003 + 0.0002 + 0.0001) / 3.0
	if math.Abs(first.Amp-wantAmp) > 1e-12 {
		t.Errorf("merged amp = %v, want %v", first.Amp, wantAmp)
	}

	// L's second artefact remains separate even though it is in no other
	// filter, and the remaining two are ordered by |SNR|.
	if merged[1].X != 400 || merged[1].Y != 100 {
		t.Errorf("second artefact = (%d,%d), want (400,100)", merged[1].X, merged[1].Y)
	}
	if merged[2].X != 700 || merged[2].Y != 700 {
		t.Errorf("third artefact = (%d,%d), want (700,700)", merged[2].X, merged[2].Y)
	}
	for i := 1; i < len(merged); i++ {
		if math.Abs(merged[i].SNR) > math.Abs(merged[i-1].SNR) {
			t.Errorf("merged list is not ordered by |snr|: %+v", merged)
		}
		if merged[i].N != i+1 {
			t.Errorf("merged[%d] has n = %d, want %d", i, merged[i].N, i+1)
		}
	}
}

func TestMergeKeepsDistantArtefactsSeparate(t *testing.T) {
	filters := []union.Filter{
		{Name: "L", Artefacts: []report.Artefact{{X: 100, Y: 100, SNR: 10}}},
		{Name: "R", Artefacts: []report.Artefact{{X: 130, Y: 100, SNR: 9}}},
	}

	if merged := union.Merge(filters, 16); len(merged) != 2 {
		t.Fatalf("merged %d artefacts, want 2 at tolerance 16: %+v", len(merged), merged)
	}
	if merged := union.Merge(filters, 40); len(merged) != 1 {
		t.Fatalf("merged %d artefacts, want 1 at tolerance 40: %+v", len(merged), merged)
	}
}

func TestMergeKeepsOneEntryPerFilter(t *testing.T) {
	// Two rows of one filter are distinct catalogue entries even when they sit
	// close together, so they must not be averaged into one.
	filters := []union.Filter{
		{Name: "L", Artefacts: []report.Artefact{
			{X: 100, Y: 100, SNR: 20},
			{X: 105, Y: 100, SNR: 15},
		}},
		{Name: "R", Artefacts: []report.Artefact{
			{X: 103, Y: 100, SNR: 10},
		}},
	}

	merged := union.Merge(filters, 16)
	if len(merged) != 2 {
		t.Fatalf("merged %d artefacts, want 2: %+v", len(merged), merged)
	}
	if merged[0].X != 100 || merged[1].X != 104 {
		t.Errorf("merged positions = %d and %d, want 100 and 104", merged[0].X, merged[1].X)
	}
}

func TestMissing(t *testing.T) {
	merged := []report.Artefact{
		{N: 1, X: 100, Y: 100},
		{N: 2, X: 400, Y: 400},
		{N: 3, X: 700, Y: 200},
	}
	filter := union.Filter{Name: "B", Artefacts: []report.Artefact{
		{X: 103, Y: 98},
		{X: 690, Y: 212},
	}}

	missing := union.Missing(merged, filter, 16)
	if len(missing) != 1 {
		t.Fatalf("missing %d entries, want 1: %+v", len(missing), missing)
	}
	if missing[0].N != 2 || missing[0].X != 400 || missing[0].Y != 400 {
		t.Errorf("missing entry = %+v, want n=2 at (400,400)", missing[0])
	}
}

func TestMissingAllWhenFilterEmpty(t *testing.T) {
	merged := []report.Artefact{{N: 1, X: 10, Y: 20}}
	if missing := union.Missing(merged, union.Filter{Name: "S"}, 16); len(missing) != 1 {
		t.Fatalf("missing %d entries, want 1", len(missing))
	}
}

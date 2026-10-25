package detect_test

import (
	"testing"

	"github.com/hoxca/exorciser/internal/detect"
)

func TestRejectCorners(t *testing.T) {
	const w, h = 1000, 800
	arts := []detect.Artefact{
		{X: 10, Y: 10},   // top left: reject
		{X: 79, Y: 79},   // top left edge: reject
		{X: 80, Y: 80},   // just past both edges: keep
		{X: 950, Y: 20},  // top right: reject
		{X: 919, Y: 20},  // 81 px from the right edge: keep
		{X: 30, Y: 750},  // bottom left: reject
		{X: 30, Y: 719},  // 80 px from the bottom edge: keep
		{X: 480, Y: 400}, // centre: keep
		{X: 120, Y: 10},  // top edge, not a corner: keep
		{X: 990, Y: 400}, // right edge, not a corner: keep
	}

	kept, rejected := detect.RejectCorners(arts, w, h, 80)
	if rejected != 4 {
		t.Errorf("rejected %d artefacts, want 4", rejected)
	}
	want := []struct{ x, y int }{
		{80, 80}, {919, 20}, {30, 719}, {480, 400}, {120, 10}, {990, 400},
	}
	if len(kept) != len(want) {
		t.Fatalf("kept %d artefacts, want %d: %+v", len(kept), len(want), kept)
	}
	for i, w := range want {
		if kept[i].X != w.x || kept[i].Y != w.y {
			t.Errorf("kept[%d] = (%d,%d), want (%d,%d)", i, kept[i].X, kept[i].Y, w.x, w.y)
		}
	}
}

func TestRejectCornersDisabled(t *testing.T) {
	arts := []detect.Artefact{{X: 0, Y: 0}, {X: 500, Y: 400}}

	kept, rejected := detect.RejectCorners(arts, 1000, 800, 0)
	if rejected != 0 {
		t.Errorf("rejected %d artefacts with size 0, want 0", rejected)
	}
	if len(kept) != len(arts) {
		t.Errorf("kept %d artefacts, want %d", len(kept), len(arts))
	}
}

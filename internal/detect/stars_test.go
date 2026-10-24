package detect_test

import (
	"math"
	"testing"

	"github.com/hoxca/exorciser/internal/detect"
)

func TestStarComponents(t *testing.T) {
	const w, h = 512, 512
	plane := syntheticPlane(w, h, []injected{
		{x: 150, y: 150, sigma: 4, amp: 0.5},   // broad, saturated core
		{x: 350, y: 350, sigma: 0.6, amp: 0.5}, // sharp, compact core
	}, 0.001, 9)

	stars, err := detect.StarComponents(plane, w, h, 0.9)
	if err != nil {
		t.Fatalf("StarComponents: %v", err)
	}
	if len(stars) != 2 {
		t.Fatalf("StarComponents returned %d stars, want 2: %+v", len(stars), stars)
	}

	big, small := stars[0], stars[1]
	if big.Area <= small.Area {
		t.Fatalf("stars are not sorted by area: %+v", stars)
	}
	if big.Area < 10 {
		t.Errorf("broad star area = %d, want at least 10", big.Area)
	}
	if small.Area > 5 {
		t.Errorf("sharp star area = %d, want at most 5", small.Area)
	}
	if math.Abs(big.X-150) > 1 || math.Abs(big.Y-150) > 1 {
		t.Errorf("broad star centre = (%.1f,%.1f), want near (150,150)", big.X, big.Y)
	}
	if math.Abs(small.X-350) > 1 || math.Abs(small.Y-350) > 1 {
		t.Errorf("sharp star centre = (%.1f,%.1f), want near (350,350)", small.X, small.Y)
	}
}

func TestRejectStarHaloes(t *testing.T) {
	stars := []detect.Star{
		{X: 100, Y: 100, Area: 5},
		{X: 500, Y: 500, Area: 100},
		{X: 900, Y: 100, Area: 10},
	}
	arts := []detect.Artefact{
		{X: 120, Y: 100}, // 20 px from the small star: reject
		{X: 100, Y: 160}, // 60 px from the small star: keep
		{X: 500, Y: 700}, // 200 px from the big star: reject
		{X: 500, Y: 730}, // 230 px from the big star: keep
		{X: 900, Y: 300}, // 200 px from a small star: keep
		{X: 500, Y: 550}, // 50 px from the big star: reject
	}

	kept, rejected := detect.RejectStarHaloes(arts, stars, 56, 220, 40)
	if rejected != 3 {
		t.Errorf("rejected %d artefacts, want 3", rejected)
	}
	want := []struct{ x, y int }{{100, 160}, {500, 730}, {900, 300}}
	if len(kept) != len(want) {
		t.Fatalf("kept %d artefacts, want %d: %+v", len(kept), len(want), kept)
	}
	for i, w := range want {
		if kept[i].X != w.x || kept[i].Y != w.y {
			t.Errorf("kept[%d] = (%d,%d), want (%d,%d)", i, kept[i].X, kept[i].Y, w.x, w.y)
		}
	}
}

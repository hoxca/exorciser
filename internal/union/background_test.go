package union_test

import (
	"math"
	"math/rand"
	"testing"

	"github.com/hoxca/exorciser/internal/report"
	"github.com/hoxca/exorciser/internal/union"
)

func noisySky(w, h int, level, noise float64, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = float32(level + rng.NormFloat64()*noise)
	}
	return plane
}

func TestFindBackground(t *testing.T) {
	const w, h = 400, 400
	plane := noisySky(w, h, 0.01, 0.001, 3)
	// A bright star that must not fall inside the chosen window.
	for y := 30; y < 70; y++ {
		for x := 30; x < 70; x++ {
			plane[y*w+x] = 1
		}
	}
	artefacts := []report.Artefact{{X: 300, Y: 300, Scale: 8}}

	bg, err := union.FindBackground(plane, w, h, artefacts, nil, 65)
	if err != nil {
		t.Fatalf("FindBackground: %v", err)
	}
	if bg.Size != 65 {
		t.Errorf("size = %d, want 65", bg.Size)
	}
	if bg.Mean < 0.009 || bg.Mean > 0.011 {
		t.Errorf("mean sky level = %v, want about 0.01", bg.Mean)
	}
	if bg.Noise <= 0 || bg.SNR <= 0 {
		t.Errorf("noise = %v and snr = %v, want positive", bg.Noise, bg.SNR)
	}
	if bg.Pixels < 3169 || bg.Pixels > 65*65 {
		t.Errorf("surviving pixels = %d, want between 3169 and %d", bg.Pixels, 65*65)
	}

	// The window must clear the artefact by three ghost sigmas.
	if d := rectDistance(bg.X, bg.Y, 32, artefacts[0].X, artefacts[0].Y, 3*artefacts[0].Scale); d <= 0 {
		t.Errorf("window centred at (%d,%d) touches the artefact clearance", bg.X, bg.Y)
	}
	// The window must avoid the bright star block.
	if bg.X+32 >= 30 && bg.X-32 < 70 && bg.Y+32 >= 30 && bg.Y-32 < 70 {
		t.Errorf("window centred at (%d,%d) overlaps the bright star", bg.X, bg.Y)
	}
}

func rectDistance(cx, cy, half, px, py int, clearance float64) float64 {
	x0, x1 := float64(cx-half), float64(cx+half)
	y0, y1 := float64(cy-half), float64(cy+half)
	dx := math.Max(0, math.Max(x0-float64(px), float64(px)-x1))
	dy := math.Max(0, math.Max(y0-float64(py), float64(py)-y1))
	return math.Hypot(dx, dy) - clearance
}

func TestFindBackgroundPrefersUniformSky(t *testing.T) {
	const w, h = 320, 320
	plane := noisySky(w, h, 0.01, 0.001, 8)
	// The left part is uniform but brighter than the median; the right part
	// has a smooth gradient with an average level equal to the sky. Uniformity
	// must win, so the gradient (nebulosity) is never chosen.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch {
			case x < 140:
				plane[y*w+x] += 0.002
			default:
				plane[y*w+x] += float32((float64(x-140)/140.0)*0.006 - 0.003)
			}
		}
	}

	bg, err := union.FindBackground(plane, w, h, nil, nil, 65)
	if err != nil {
		t.Fatalf("FindBackground: %v", err)
	}
	if bg.X >= 140 {
		t.Errorf("background centre x = %d, want a uniform window left of the gradient", bg.X)
	}

	// The chosen window must itself be flat across its quadrants.
	q := quadrantSpread(plane, w, bg.X, bg.Y)
	if q > 0.0005 {
		t.Errorf("chosen window quadrant spread = %v, want a uniform patch", q)
	}
}

// quadrantSpread returns max-min of the four corner quadrant means.
func quadrantSpread(plane []float32, w, cx, cy int) float64 {
	means := make([]float64, 0, 4)
	for _, ys := range [][2]int{{cy - 32, cy - 1}, {cy + 1, cy + 32}} {
		for _, xs := range [][2]int{{cx - 32, cx - 1}, {cx + 1, cx + 32}} {
			var sum float64
			var n int
			for y := ys[0]; y <= ys[1]; y++ {
				for x := xs[0]; x <= xs[1]; x++ {
					sum += float64(plane[y*w+x])
					n++
				}
			}
			means = append(means, sum/float64(n))
		}
	}
	min, max := math.Inf(1), math.Inf(-1)
	for _, v := range means {
		min = math.Min(min, v)
		max = math.Max(max, v)
	}
	return max - min
}

func TestFindBackgroundTreatsMaskedStars(t *testing.T) {
	const w, h = 300, 300
	plane := noisySky(w, h, 0.01, 0.001, 9)
	mask := make([]bool, w*h)
	for y := 100; y < 140; y++ {
		for x := 100; x < 140; x++ {
			mask[y*w+x] = true
		}
	}

	bg, err := union.FindBackground(plane, w, h, nil, mask, 65)
	if err != nil {
		t.Fatalf("FindBackground: %v", err)
	}
	if bg.Pixels < 3169 {
		t.Errorf("surviving pixels = %d, want at least 3169", bg.Pixels)
	}
	// No quadrant may collapse below its minimum survivor count.
	for _, ys := range [][2]int{{bg.Y - 32, bg.Y - 1}, {bg.Y + 1, bg.Y + 32}} {
		for _, xs := range [][2]int{{bg.X - 32, bg.X - 1}, {bg.X + 1, bg.X + 32}} {
			var alive int
			for y := ys[0]; y <= ys[1]; y++ {
				for x := xs[0]; x <= xs[1]; x++ {
					if !mask[y*w+x] {
						alive++
					}
				}
			}
			if alive < 256 {
				t.Errorf("quadrant %v has %d unmasked pixels, want at least 256", []int{xs[0], ys[0]}, alive)
			}
		}
	}
}

func TestFindBackgroundSkipsMissingPixels(t *testing.T) {
	const w, h = 300, 300
	plane := noisySky(w, h, 0.01, 0.001, 6)
	for y := 100; y < 140; y++ {
		for x := 100; x < 140; x++ {
			plane[y*w+x] = float32(math.NaN())
		}
	}

	bg, err := union.FindBackground(plane, w, h, nil, nil, 65)
	if err != nil {
		t.Fatalf("FindBackground: %v", err)
	}
	for y := bg.Y - 32; y <= bg.Y+32; y++ {
		for x := bg.X - 32; x <= bg.X+32; x++ {
			if math.IsNaN(float64(plane[y*w+x])) {
				t.Fatalf("background window centred at (%d,%d) contains a missing pixel at (%d,%d)",
					bg.X, bg.Y, x, y)
			}
		}
	}
}

func TestFindBackgroundAvoidsArtefacts(t *testing.T) {
	const w, h = 200, 200
	plane := noisySky(w, h, 0.02, 0.001, 4)

	// A grid of artefacts covering every candidate position.
	var artefacts []report.Artefact
	for y := 20; y < h; y += 20 {
		for x := 20; x < w; x += 20 {
			artefacts = append(artefacts, report.Artefact{X: x, Y: y, Scale: 10})
		}
	}
	if _, err := union.FindBackground(plane, w, h, artefacts, nil, 65); err == nil {
		t.Fatal("FindBackground succeeded with no artefact-free window, want an error")
	}
}

func TestFindBackgroundRejectsBadSize(t *testing.T) {
	plane := noisySky(200, 200, 0.01, 0.001, 5)
	for _, size := range []int{0, 2, 64, 201} {
		if _, err := union.FindBackground(plane, 200, 200, nil, nil, size); err == nil {
			t.Errorf("FindBackground accepted size %d, want an error", size)
		}
	}
}

func TestFindBackgroundRejectsFlatImage(t *testing.T) {
	plane := make([]float32, 200*200)
	for i := range plane {
		plane[i] = 0.5
	}
	if _, err := union.FindBackground(plane, 200, 200, nil, nil, 65); err == nil {
		t.Fatal("FindBackground accepted a noise-free image, want an error")
	}
}

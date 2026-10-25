package erase_test

import (
	"math"
	"math/rand"
	"testing"

	"github.com/hoxca/exorciser/internal/erase"
)

func sky(w, h int, level, noise float64, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = float32(level + rng.NormFloat64()*noise)
	}
	return plane
}

func blob(plane []float32, w, cx, cy int, amp, sigma float64) {
	for y := cy - 4*int(sigma); y <= cy+4*int(sigma); y++ {
		for x := cx - 4*int(sigma); x <= cx+4*int(sigma); x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			plane[y*w+x] += float32(amp * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
		}
	}
}

func TestWindowStatsClipsStars(t *testing.T) {
	const w, h = 200, 200
	plane := sky(w, h, 0.01, 0.001, 1)
	// A bright star inside the window must not bias the measurement.
	for y := 90; y < 110; y++ {
		for x := 90; x < 110; x++ {
			plane[y*w+x] = 1
		}
	}

	st, err := erase.WindowStats(plane, w, h, 100, 100, 65)
	if err != nil {
		t.Fatalf("WindowStats: %v", err)
	}
	if math.Abs(st.Mean-0.01) > 0.0002 {
		t.Errorf("mean = %v, want about 0.01", st.Mean)
	}
	if math.Abs(st.Noise-0.001) > 0.0002 {
		t.Errorf("noise = %v, want about 0.001", st.Noise)
	}
	if st.Pixels >= 65*65 {
		t.Errorf("surviving pixels = %d, want the star clipped", st.Pixels)
	}
}

func TestWindowStatsRejectsBadWindow(t *testing.T) {
	plane := sky(200, 200, 0.01, 0.001, 2)
	if _, err := erase.WindowStats(plane, 200, 200, 10, 10, 65); err == nil {
		t.Error("WindowStats accepted a window crossing the border")
	}
	if _, err := erase.WindowStats(plane, 200, 200, 100, 100, 64); err == nil {
		t.Error("WindowStats accepted an even window size")
	}
}

func TestInpaintRemovesGhostAndMatchesSky(t *testing.T) {
	const w, h = 200, 200
	const (
		cleanLevel  = 0.01
		cleanNoise  = 0.0005
		targetLevel = 0.02
		targetNoise = 0.002
		radius      = 20
		feather     = 4
	)

	clean := sky(w, h, cleanLevel, cleanNoise, 3)
	// A star that must survive the inpainting.
	blob(clean, w, 92, 92, 0.05, 2)

	target := sky(w, h, targetLevel, targetNoise, 4)
	// The ghost being removed.
	blob(target, w, 100, 100, 0.05, 6)

	before := make([]float32, len(target))
	copy(before, target)

	cal := erase.Calibration{
		TargetMean:  targetLevel,
		TargetNoise: targetNoise,
		CleanMean:   cleanLevel,
		CleanNoise:  cleanNoise,
	}
	if err := erase.Inpaint(target, clean, w, h, 100, 100, radius, feather, cal,
		rand.New(rand.NewSource(7))); err != nil {
		t.Fatalf("Inpaint: %v", err)
	}

	// The ghost core is gone: the result is the target level plus whatever
	// structure the clean reference has there, not +0.05.
	want := targetLevel + (float64(clean[100*w+100]) - cleanLevel)
	if math.Abs(float64(target[100*w+100])-want) > 4*targetNoise {
		t.Errorf("centre = %v, want %v (target level plus clean structure)",
			target[100*w+100], want)
	}

	// The clean reference's star at (92,92) is transferred at the right scale.
	star := float64(target[92*w+92])
	if star < targetLevel+0.03 {
		t.Errorf("transferred star = %v, want about %v", star, targetLevel+0.05)
	}

	// The patch noise matches the target's noise.
	var sum, sq float64
	var n int
	for y := 80; y < 90; y++ {
		for x := 105; x < 115; x++ {
			v := float64(target[y*w+x])
			sum += v
			sq += v * v
			n++
		}
	}
	mean := sum / float64(n)
	got := math.Sqrt(sq/float64(n) - mean*mean)
	if math.Abs(got-targetNoise) > 0.25*targetNoise {
		t.Errorf("patch noise = %v, want about %v", got, targetNoise)
	}

	// Outside the disc nothing changes.
	for _, p := range [][2]int{{100 + radius + 1, 100}, {100, 100 + radius + 1}, {10, 10}} {
		if target[p[1]*w+p[0]] != before[p[1]*w+p[0]] {
			t.Errorf("pixel %v outside the disc changed from %v to %v",
				p, before[p[1]*w+p[0]], target[p[1]*w+p[0]])
		}
	}
	// The rim itself has zero weight.
	rim := 100*w + 100 + radius
	if target[rim] != before[rim] {
		t.Errorf("rim pixel changed from %v to %v", before[rim], target[rim])
	}
}

func TestInpaintIsDeterministic(t *testing.T) {
	const w, h = 200, 200
	clean := sky(w, h, 0.01, 0.0005, 5)
	target := sky(w, h, 0.02, 0.002, 6)
	cal := erase.Calibration{TargetMean: 0.02, TargetNoise: 0.002, CleanMean: 0.01, CleanNoise: 0.0005}

	first := make([]float32, len(target))
	copy(first, target)
	if err := erase.Inpaint(first, clean, w, h, 100, 100, 20, 4, cal,
		rand.New(rand.NewSource(42))); err != nil {
		t.Fatal(err)
	}
	second := make([]float32, len(target))
	copy(second, target)
	if err := erase.Inpaint(second, clean, w, h, 100, 100, 20, 4, cal,
		rand.New(rand.NewSource(42))); err != nil {
		t.Fatal(err)
	}
	third := make([]float32, len(target))
	copy(third, target)
	if err := erase.Inpaint(third, clean, w, h, 100, 100, 20, 4, cal,
		rand.New(rand.NewSource(43))); err != nil {
		t.Fatal(err)
	}

	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("same seed produced different values at %d: %v and %v", i, first[i], second[i])
		}
	}
	same := true
	for i := 100*w + 80; i < 100*w+120; i++ {
		if first[i] != third[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("different seeds produced identical noise")
	}
}

func TestInpaintRejectsBadArguments(t *testing.T) {
	plane := sky(100, 100, 0.01, 0.001, 7)
	cal := erase.Calibration{TargetMean: 0.01, TargetNoise: 0.001, CleanMean: 0.01, CleanNoise: 0.0005}
	rng := rand.New(rand.NewSource(1))

	if err := erase.Inpaint(plane, plane, 100, 100, 50, 50, 0, 0, cal, rng); err == nil {
		t.Error("Inpaint accepted radius 0")
	}
	if err := erase.Inpaint(plane, plane, 100, 100, 50, 50, 10, 11, cal, rng); err == nil {
		t.Error("Inpaint accepted feather greater than radius")
	}
	if err := erase.Inpaint(plane, plane, 100, 100, 50, 50, 10, 2, cal, nil); err == nil {
		t.Error("Inpaint accepted a nil noise source")
	}
}

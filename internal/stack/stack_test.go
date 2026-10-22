package stack_test

import (
	"math"
	"math/rand"
	"testing"

	"github.com/hoxca/exorciser/internal/stack"
)

func TestNormalize(t *testing.T) {
	const w, h = 512, 512
	ref := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ref[y*w+x] = float32(0.1 + 0.0001*float64(x) + 0.0002*float64(y))
		}
	}
	frame := make([]float32, w*h)
	for i, v := range ref {
		frame[i] = 2*v + 10
	}

	aff, err := stack.Normalize(frame, ref, w, h, 8)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if math.Abs(aff.A-2) > 1e-3 || math.Abs(aff.B-10) > 1e-2 {
		t.Errorf("affine = %+v, want a=2 b=10", aff)
	}
	for i := range ref {
		if got, want := frame[i], ref[i]; math.Abs(float64(got-want)) > 1e-5 {
			t.Fatalf("pixel %d = %v, want %v", i, got, want)
		}
	}
}

func TestAverageRejectsOutlier(t *testing.T) {
	const w, h = 8, 8
	base := []float64{0.90, 0.92, 0.94, 0.96, 0.98, 1.00, 1.02, 1.04, 1.06, 1.08}
	frames := make([][]float32, len(base))
	for k := range frames {
		frames[k] = make([]float32, w*h)
		for i := range frames[k] {
			frames[k][i] = float32(base[k])
		}
	}
	frames[8][0] = 1000 // a cosmic ray in one frame

	res, err := stack.Average(frames, w, h, stack.Options{
		Sigma: 3, Iterations: 3, MinValid: 2, Workers: 2,
	})
	if err != nil {
		t.Fatalf("Average: %v", err)
	}
	want := (base[0] + base[1] + base[2] + base[3] + base[4] + base[5] + base[6] + base[7] + base[9]) / 9
	if got := float64(res.Plane[0]); math.Abs(got-want) > 1e-5 {
		t.Errorf("clipped pixel = %v, want %v", got, want)
	}
	if res.Rejected != 1 {
		t.Errorf("rejected = %d, want 1", res.Rejected)
	}
	if res.Invalid != 0 {
		t.Errorf("invalid = %d, want 0", res.Invalid)
	}
	if got := float64(res.Plane[1]); math.Abs(got-0.99) > 1e-5 {
		t.Errorf("clean pixel = %v, want 0.99", got)
	}
}

func TestAverageMinValid(t *testing.T) {
	const w, h = 4, 4
	frames := make([][]float32, 3)
	for k := range frames {
		frames[k] = make([]float32, w*h)
		for i := range frames[k] {
			frames[k][i] = 1
		}
	}
	frames[1][5] = float32(math.NaN())
	frames[2][5] = float32(math.NaN())

	res, err := stack.Average(frames, w, h, stack.Options{MinValid: 2, Workers: 1})
	if err != nil {
		t.Fatalf("Average: %v", err)
	}
	if !math.IsNaN(float64(res.Plane[5])) {
		t.Errorf("pixel with one sample = %v, want NaN", res.Plane[5])
	}
	if res.Invalid != 1 {
		t.Errorf("invalid = %d, want 1", res.Invalid)
	}
	if got := res.Plane[0]; got != 1 {
		t.Errorf("valid pixel = %v, want 1", got)
	}
}

func TestAverageDeterministicAcrossWorkers(t *testing.T) {
	const w, h = 64, 64
	rng := rand.New(rand.NewSource(7))
	frames := make([][]float32, 10)
	for k := range frames {
		frames[k] = make([]float32, w*h)
		for i := range frames[k] {
			frames[k][i] = float32(1 + rng.NormFloat64()*0.01)
		}
	}
	one, err := stack.Average(frames, w, h, stack.Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	four, err := stack.Average(frames, w, h, stack.Options{Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := range one.Plane {
		if one.Plane[i] != four.Plane[i] {
			t.Fatalf("pixel %d: workers 1 = %v, workers 4 = %v", i, one.Plane[i], four.Plane[i])
		}
	}
}

func TestSubtract(t *testing.T) {
	a := []float32{1, float32(math.NaN()), 3}
	b := []float32{0.5, 2, float32(math.NaN())}
	out, err := stack.Subtract(a, b)
	if err != nil {
		t.Fatalf("Subtract: %v", err)
	}
	if out[0] != 0.5 {
		t.Errorf("out[0] = %v, want 0.5", out[0])
	}
	if !math.IsNaN(float64(out[1])) || !math.IsNaN(float64(out[2])) {
		t.Errorf("NaN did not propagate: %v", out)
	}
	if _, err := stack.Subtract(a, b[:2]); err == nil {
		t.Fatal("Subtract accepted different lengths")
	}
}

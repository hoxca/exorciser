package detect

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

const (
	testW = 512
	testH = 512
)

// synthetic returns a w×h plane of a smooth sky gradient plus Gaussian noise.
// The gradient matters: the affine fit regresses block medians, and against a
// structureless reference those medians carry nothing but their own sampling
// noise, which attenuates the slope. Real frames always have a sky gradient,
// vignetting and objects. The ramp may dip below zero in one corner, which is
// harmless for an affine fit.
func synthetic(level, sigma float64, seed uint64) []float32 {
	r := rand.New(rand.NewPCG(seed, seed+1))
	p := make([]float32, testW*testH)
	for y := 0; y < testH; y++ {
		v := float64(y)/testH - 0.5
		for x := 0; x < testW; x++ {
			u := float64(x)/testW - 0.5
			sky := level + 20*sigma*(u+0.3*v)
			p[y*testW+x] = float32(sky + sigma*r.NormFloat64())
		}
	}
	return p
}

// addGhost adds a circular Gaussian of the given peak amplitude and sigma.
func addGhost(p []float32, cx, cy int, amp, sigma float64) {
	radius := int(math.Ceil(4 * sigma))
	for y := max(cy-radius, 0); y <= min(cy+radius, testH-1); y++ {
		for x := max(cx-radius, 0); x <= min(cx+radius, testW-1); x++ {
			d2 := float64((x-cx)*(x-cx) + (y-cy)*(y-cy))
			p[y*testW+x] += float32(amp * math.Exp(-d2/(2*sigma*sigma)))
		}
	}
}

// paired returns a reference and a frame of the same sky with independent
// noise: frame = 0.8*sky + 2e-3 + noise. Sharing the sky rather than the noise
// matters: real exposures are independent, so the residual carries both
// frames' noise.
func paired(sigma float64, seed uint64) (ref, frame []float32) {
	r := rand.New(rand.NewPCG(seed, seed+1))
	q := rand.New(rand.NewPCG(seed*13+5, seed*7+3))
	ref = make([]float32, testW*testH)
	frame = make([]float32, len(ref))
	for y := 0; y < testH; y++ {
		v := float64(y)/testH - 0.5
		for x := 0; x < testW; x++ {
			u := float64(x)/testW - 0.5
			sky := 0.01 + 20*sigma*(u+0.3*v)
			ref[y*testW+x] = float32(sky + sigma*r.NormFloat64())
			frame[y*testW+x] = float32(0.8*sky + 2e-3 + sigma*q.NormFloat64())
		}
	}
	return ref, frame
}

func TestFitRecoversAffine(t *testing.T) {
	ref, frame := paired(0.004, 1)

	// Cosmic rays in the frame alone must be clipped, not fitted.
	r := rand.New(rand.NewPCG(99, 100))
	for range 200 {
		frame[r.IntN(len(frame))] += 0.2
	}

	aff, err := Fit(frame, ref, testW, testH, 8)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if math.Abs(aff.A-0.8) > 2e-2 {
		t.Errorf("A = %v, want 0.8", aff.A)
	}
	if math.Abs(aff.B-2e-3) > 2e-4 {
		t.Errorf("B = %v, want 2e-3", aff.B)
	}
}

func TestFitRejectsFlatReference(t *testing.T) {
	frame := synthetic(0.01, 0.004, 2)
	ref := make([]float32, testW*testH)

	_, err := Fit(frame, ref, testW, testH, 8)
	if err == nil {
		t.Fatal("Fit of a flat reference succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "flat") {
		t.Errorf("error = %q, want a flat-frame message", err)
	}
}

func TestFitRejectsTinyImages(t *testing.T) {
	frame := synthetic(0.01, 0.004, 3)
	ref := synthetic(0.01, 0.004, 4)

	if _, err := Fit(frame, ref, 10, 10, 8); err == nil {
		t.Fatal("Fit of a 10×10 image succeeded, want an error")
	}
}

func TestScoreFindsGhost(t *testing.T) {
	tests := []struct {
		name  string
		ghost float64
		amp   float64
		tol   int
	}{
		{name: "matched 8 px", ghost: 8, amp: 0.0028, tol: 4},
		{name: "matched 16 px", ghost: 16, amp: 0.0014, tol: 8},
	}

	// The residual carries both frames' noise: sqrt(1 + 0.8^2) * frameSigma.
	const frameSigma = 0.004
	residual := frameSigma * math.Sqrt(1+0.8*0.8)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, frame := paired(frameSigma, 5)
			const gx, gy = 300, 260
			addGhost(frame, gx, gy, tt.amp, tt.ghost)

			res, err := Score(frame, ref, testW, testH, Options{
				Window: At(gx, gy, 128),
				Margin: 64,
				Scales: []float64{4, 8, 16},
			})
			if err != nil {
				t.Fatalf("Score: %v", err)
			}

			// A Gaussian ghost filtered by a matching kernel keeps half its peak,
			// and the effective number of pixels is 4*pi*sigma^2.
			want := tt.amp * math.Sqrt(math.Pi) * tt.ghost / residual
			if math.Abs(res.PeakSNR-want) > 0.2*want {
				t.Errorf("PeakSNR = %.2f, want %.2f (±20%%)", res.PeakSNR, want)
			}
			if res.PeakSNR <= 0 || res.PeakAmp <= 0 {
				t.Errorf("PeakSNR = %.2f, PeakAmp = %v, want both positive",
					res.PeakSNR, res.PeakAmp)
			}
			if res.Scale != tt.ghost {
				t.Errorf("Scale = %v, want %v", res.Scale, tt.ghost)
			}
			if abs(res.PeakX-gx) > tt.tol || abs(res.PeakY-gy) > tt.tol {
				t.Errorf("peak at (%d,%d), want (%d,%d) ±%d",
					res.PeakX, res.PeakY, gx, gy, tt.tol)
			}
			if res.Noise <= 0 {
				t.Errorf("Noise = %v, want positive", res.Noise)
			}
			if math.Abs(res.Affine.A-0.8) > 2e-2 {
				t.Errorf("A = %v, want 0.8", res.Affine.A)
			}
		})
	}
}

func TestScoreNull(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		ref, frame := paired(0.004, seed)

		res, err := Score(frame, ref, testW, testH, Options{
			Window: At(256, 256, 128),
			Margin: 64,
			Scales: []float64{4, 8, 16},
		})
		if err != nil {
			t.Fatalf("seed %d: Score: %v", seed, err)
		}
		if res.PeakSNR < 0 || res.PeakSNR > 5.5 {
			t.Errorf("seed %d: PeakSNR = %.2f on a ghost-free pair, want 0 <= snr <= 5.5",
				seed, res.PeakSNR)
		}
	}
}

func TestScoreIgnoresGhostOutsideWindow(t *testing.T) {
	ref, frame := paired(0.004, 7)
	addGhost(frame, 120, 120, 0.0014, 8)

	res, err := Score(frame, ref, testW, testH, Options{
		Window: At(300, 260, 128),
		Margin: 64,
		Scales: []float64{4, 8, 16},
	})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.PeakSNR < 0 || res.PeakSNR > 5 {
		t.Errorf("PeakSNR = %.2f for a ghost outside the window, want 0 <= snr <= 5", res.PeakSNR)
	}
}

func TestScoreIgnoresDarkGhost(t *testing.T) {
	ref, frame := paired(0.004, 17)

	// Same strength as the bright ghost above, but dark. The matched response
	// is strongly negative, so Score must ignore it and report only noise.
	addGhost(frame, 300, 260, -0.0028, 8)

	res, err := Score(frame, ref, testW, testH, Options{
		Window: At(300, 260, 128),
		Margin: 64,
		Scales: []float64{4, 8, 16},
	})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.PeakSNR < 0 || res.PeakAmp < 0 {
		t.Errorf("PeakSNR = %.2f, PeakAmp = %v, want both non-negative",
			res.PeakSNR, res.PeakAmp)
	}
	// The dark ghost alone would score about -8, well past the detection
	// threshold in magnitude. Only a positive noise peak may remain.
	if res.PeakSNR > 5.5 {
		t.Errorf("PeakSNR = %.2f on a dark ghost, want a noise-level positive response",
			res.PeakSNR)
	}
}

func TestScoreMasksBrightStar(t *testing.T) {
	ref, frame := paired(0.004, 9)

	// A star in the reference only, 35 sigma at its peak.
	addGhost(ref, 300, 260, 0.2, 2)

	res, err := Score(frame, ref, testW, testH, Options{
		Window: At(300, 260, 128),
		Margin: 64,
		Scales: []float64{4, 8, 16},
	})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.PeakSNR < 0 || res.PeakSNR > 5 {
		t.Errorf("PeakSNR = %.2f with a masked star, want 0 <= snr <= 5", res.PeakSNR)
	}
}

func TestScoreRejectsSmallMargin(t *testing.T) {
	ref, frame := paired(0.004, 11)

	_, err := Score(frame, ref, testW, testH, Options{
		Window: At(256, 256, 128),
		Margin: 16,
		Scales: []float64{8},
	})
	if err == nil {
		t.Fatal("Score with a margin smaller than the kernel succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "margin") {
		t.Errorf("error = %q, want a margin message", err)
	}
}

func TestScoreRejectsWindowNearEdge(t *testing.T) {
	ref, frame := paired(0.004, 13)

	_, err := Score(frame, ref, testW, testH, Options{
		Window: At(40, 40, 128),
		Margin: 64,
		Scales: []float64{8},
	})
	if err == nil {
		t.Fatal("Score on a window that cannot hold its margin succeeded, want an error")
	}
}

func TestMaxKernelRadius(t *testing.T) {
	if got, want := MaxKernelRadius([]float64{4, 8, 16}), 48; got != want {
		t.Errorf("MaxKernelRadius = %d, want %d", got, want)
	}
	if got, want := MaxKernelRadius([]float64{6.5}), 20; got != want {
		t.Errorf("MaxKernelRadius of 6.5 = %d, want %d", got, want)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

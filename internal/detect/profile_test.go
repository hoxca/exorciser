package detect

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestExtractProfile(t *testing.T) {
	ref, frame := paired(0.004, 31)
	const gx, gy = 300, 260
	addGhost(frame, gx, gy, 0.004, 5)

	p, err := ExtractProfile(frame, ref, testW, testH, ProfileOptions{
		X: gx, Y: gy, Radius: 16, Margin: 64,
	})
	if err != nil {
		t.Fatalf("ExtractProfile: %v", err)
	}
	if p.Radius != 16 {
		t.Errorf("Radius = %d, want 16", p.Radius)
	}
	side := 2*p.Radius + 1
	if len(p.Kernel) != side*side {
		t.Fatalf("Kernel has %d pixels, want %d", len(p.Kernel), side*side)
	}
	var sum float64
	for _, v := range p.Kernel {
		if v < 0 || !finite(v) {
			t.Fatalf("Kernel contains %v, want finite non-negative weights", v)
		}
		sum += v
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("Kernel sums to %v, want 1", sum)
	}
	centre := p.Kernel[(side*side)/2]
	if centre <= 0 {
		t.Error("Kernel centre is not positive")
	}
	if p.Kernel[0] >= centre {
		t.Errorf("Kernel corner = %v, want it below the centre %v", p.Kernel[0], centre)
	}
	if abs(p.ShiftX) > 2 || abs(p.ShiftY) > 2 {
		t.Errorf("recentred by (%d,%d), want no more than 2 px", p.ShiftX, p.ShiftY)
	}
	if p.Noise <= 0 {
		t.Errorf("Noise = %v, want positive", p.Noise)
	}
}

func TestExtractProfileRejectsDarkPatch(t *testing.T) {
	ref, frame := paired(0.004, 32)
	addGhost(frame, 300, 260, -0.004, 5)

	_, err := ExtractProfile(frame, ref, testW, testH, ProfileOptions{
		X: 300, Y: 260, Radius: 16, Margin: 64,
	})
	if err == nil {
		t.Fatal("ExtractProfile accepted a dark patch, want an error")
	}
	if !strings.Contains(err.Error(), "positive profile") {
		t.Errorf("error = %q, want a positive-profile message", err)
	}
}

func TestExtractProfileRejectsMaskedCentre(t *testing.T) {
	ref, frame := paired(0.004, 33)

	// A star in the reference only becomes a strong negative residual in the
	// dirty frame, so the clipping mask covers the requested centre.
	addGhost(ref, 300, 260, 0.2, 2)

	_, err := ExtractProfile(frame, ref, testW, testH, ProfileOptions{
		X: 300, Y: 260, Radius: 16, Margin: 64,
	})
	if err == nil {
		t.Fatal("ExtractProfile accepted a masked centre, want an error")
	}
	if !strings.Contains(err.Error(), "masked") {
		t.Errorf("error = %q, want a masked-centre message", err)
	}
}

func TestExtractProfileRejectsSmallMargin(t *testing.T) {
	ref, frame := paired(0.004, 34)

	_, err := ExtractProfile(frame, ref, testW, testH, ProfileOptions{
		X: 300, Y: 260, Radius: 16, Margin: 16,
	})
	if err == nil {
		t.Fatal("ExtractProfile accepted a margin equal to the radius, want an error")
	}
	if !strings.Contains(err.Error(), "margin") {
		t.Errorf("error = %q, want a margin message", err)
	}
}

func TestScoreProfileWinsForCompactGhost(t *testing.T) {
	// A compact ghost is a poor match for the only Gaussian scale, so the
	// extracted profile must take over.
	ref, frame := paired(0.004, 35)
	addGhost(frame, 300, 260, 0.012, 2)
	profile, err := ExtractProfile(frame, ref, testW, testH, ProfileOptions{
		X: 300, Y: 260, Radius: 6, Margin: 64,
	})
	if err != nil {
		t.Fatalf("ExtractProfile: %v", err)
	}

	ref, frame = paired(0.004, 36)
	const gx, gy = 320, 270
	addGhost(frame, gx, gy, 0.012, 2)

	// The compact core would be clipped by the default 4-sigma Gaussian mask;
	// the profile's looser default clip keeps it.
	res, err := Score(frame, ref, testW, testH, Options{
		Window:  At(310, 265, 128),
		Margin:  64,
		Scales:  []float64{16},
		Profile: &profile,
	})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.Kernel != KernelProfile {
		t.Errorf("Kernel = %q, want %q", res.Kernel, KernelProfile)
	}
	if res.PeakSNR <= 0 || res.PeakAmp <= 0 {
		t.Errorf("PeakSNR = %.2f, PeakAmp = %v, want both positive", res.PeakSNR, res.PeakAmp)
	}
	if abs(res.PeakX-gx) > 3 || abs(res.PeakY-gy) > 3 {
		t.Errorf("peak at (%d,%d), want (%d,%d) ±3", res.PeakX, res.PeakY, gx, gy)
	}
}

func TestScoreProfileIgnoresDarkGhost(t *testing.T) {
	ref, frame := paired(0.004, 37)
	addGhost(frame, 300, 260, 0.006, 2)
	profile, err := ExtractProfile(frame, ref, testW, testH, ProfileOptions{
		X: 300, Y: 260, Radius: 6, Margin: 64,
	})
	if err != nil {
		t.Fatalf("ExtractProfile: %v", err)
	}

	ref, frame = paired(0.004, 38)
	addGhost(frame, 300, 260, -0.006, 2)

	res, err := Score(frame, ref, testW, testH, Options{
		Window:  At(300, 260, 128),
		Margin:  64,
		Scales:  []float64{16},
		Profile: &profile,
	})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.PeakSNR < 0 || res.PeakAmp < 0 {
		t.Errorf("PeakSNR = %.2f, PeakAmp = %v, want both non-negative", res.PeakSNR, res.PeakAmp)
	}
	if res.PeakSNR > 5.5 {
		t.Errorf("PeakSNR = %.2f on a dark ghost, want a noise-level response", res.PeakSNR)
	}
}

func TestProfileScaled(t *testing.T) {
	p := testProfile(16, 8)
	tests := []struct {
		factor float64
		radius int
	}{
		{0.5, 8},
		{1, 16},
		{1.25, 20},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.factor), func(t *testing.T) {
			got, err := p.Scaled(tt.factor)
			if err != nil {
				t.Fatalf("Scaled: %v", err)
			}
			if got.Radius != tt.radius {
				t.Fatalf("Radius = %d, want %d", got.Radius, tt.radius)
			}
			side := 2*tt.radius + 1
			if len(got.Kernel) != side*side {
				t.Fatalf("kernel has %d samples, want %d", len(got.Kernel), side*side)
			}
			var sum float64
			peak := 0.0
			for i, v := range got.Kernel {
				if v < 0 || !finite(v) {
					t.Fatalf("kernel[%d] = %v, want finite non-negative", i, v)
				}
				peak = math.Max(peak, v)
				sum += v
			}
			if math.Abs(sum-1) > 1e-9 {
				t.Errorf("kernel sums to %v, want 1", sum)
			}
			if got.Kernel[(side*side)/2] != peak {
				t.Errorf("centre is not the maximum")
			}
		})
	}
}

func TestProfileScaledRejectsBadFactor(t *testing.T) {
	p := testProfile(8, 4)
	for _, factor := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := p.Scaled(factor); err == nil {
			t.Errorf("Scaled(%v) succeeded, want an error", factor)
		}
	}
}

// testProfile builds a normalised Gaussian profile for the scaling tests.
func testProfile(radius int, sigma float64) Profile {
	side := 2*radius + 1
	kernel := make([]float64, side*side)
	var sum float64
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			dx, dy := float64(x-radius), float64(y-radius)
			if dx*dx+dy*dy > float64(radius*radius) {
				continue
			}
			v := math.Exp(-(dx*dx + dy*dy) / (2 * sigma * sigma))
			kernel[y*side+x] = v
			sum += v
		}
	}
	for i := range kernel {
		kernel[i] /= sum
	}
	return Profile{Radius: radius, Kernel: kernel}
}

func TestScoreRejectsBadProfile(t *testing.T) {
	ref, frame := paired(0.004, 39)
	tests := []struct {
		name string
		p    *Profile
		want string
	}{
		{name: "radius", p: &Profile{Radius: 0, Kernel: []float64{1}}, want: "radius"},
		{name: "length", p: &Profile{Radius: 2, Kernel: []float64{1}}, want: "pixels"},
		{name: "negative", p: &Profile{Radius: 1, Kernel: []float64{-1, 0, 0, 0, 0, 0, 0, 0, 0}}, want: "negative"},
		{name: "empty", p: &Profile{Radius: 1, Kernel: make([]float64, 9)}, want: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Score(frame, ref, testW, testH, Options{
				Window:  At(256, 256, 128),
				Margin:  64,
				Scales:  []float64{8},
				Profile: tt.p,
			})
			if err == nil {
				t.Fatal("Score accepted a bad profile, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestScoreRejectsBadProfileClip(t *testing.T) {
	ref, frame := paired(0.004, 41)
	const gx, gy = 300, 260
	addGhost(frame, gx, gy, 0.004, 5)
	profile, err := ExtractProfile(frame, ref, testW, testH, ProfileOptions{
		X: gx, Y: gy, Radius: 16, Margin: 64,
	})
	if err != nil {
		t.Fatalf("ExtractProfile: %v", err)
	}

	_, err = Score(frame, ref, testW, testH, Options{
		Window:           At(300, 260, 128),
		Margin:           64,
		Scales:           []float64{8},
		Profile:          &profile,
		ProfileClipSigma: -1,
	})
	if err == nil {
		t.Fatal("Score accepted a negative profile clip sigma, want an error")
	}
	if !strings.Contains(err.Error(), "profile clip sigma") {
		t.Errorf("error = %q, want a profile-clip message", err)
	}
}

func TestScoreSearchRadius(t *testing.T) {
	ref, frame := paired(0.004, 40)
	const gx, gy = 340, 300
	addGhost(frame, gx, gy, 0.0028, 8)

	base := Options{
		Window: At(300, 260, 128),
		Margin: 64,
		Scales: []float64{8},
	}
	narrow := base
	narrow.SearchRadius = 16
	res, err := Score(frame, ref, testW, testH, narrow)
	if err != nil {
		t.Fatalf("Score narrow: %v", err)
	}
	if res.PeakSNR > 5.5 {
		t.Errorf("PeakSNR = %.2f outside the search radius, want a noise-level response", res.PeakSNR)
	}

	res, err = Score(frame, ref, testW, testH, base)
	if err != nil {
		t.Fatalf("Score full window: %v", err)
	}
	if res.PeakSNR < 5 {
		t.Errorf("PeakSNR = %.2f for a ghost in the window, want at least 5", res.PeakSNR)
	}
}

func TestScoreSearchRadiusIsCircular(t *testing.T) {
	ref, frame := paired(0.004, 41)
	// Window centre is (300,260). The ghost is at (+20,+20), distance 28.3:
	// inside a 30 px square, outside a 20 px circle.
	addGhost(frame, 320, 280, 0.05, 2)

	opts := Options{
		Window:       At(300, 260, 128),
		Margin:       64,
		Scales:       []float64{2},
		SearchRadius: 20,
		ClipSigma:    20,
	}
	res, err := Score(frame, ref, testW, testH, opts)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.PeakSNR > 5 {
		t.Errorf("PeakSNR = %.2f for a source outside the circular radius, want noise", res.PeakSNR)
	}

	opts.SearchRadius = 40
	res, err = Score(frame, ref, testW, testH, opts)
	if err != nil {
		t.Fatalf("Score wide: %v", err)
	}
	if res.PeakSNR < 6 {
		t.Errorf("PeakSNR = %.2f for a source inside the circular radius, want detection", res.PeakSNR)
	}

	opts.SearchGuard = 17 // effective radius 28, source at 28.3
	res, err = Score(frame, ref, testW, testH, opts)
	if err != nil {
		t.Fatalf("Score guarded: %v", err)
	}
	if res.PeakSNR > 5 {
		t.Errorf("PeakSNR = %.2f at the search boundary, want it rejected", res.PeakSNR)
	}
}

func TestScoreRejectsBadSearchGuard(t *testing.T) {
	ref, frame := paired(0.004, 42)
	_, err := Score(frame, ref, testW, testH, Options{
		Window: At(300, 260, 128), Margin: 64, Scales: []float64{8},
		SearchRadius: 10, SearchGuard: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "search guard") {
		t.Fatalf("error = %v, want a search-guard message", err)
	}
}

package detect_test

import (
	"math"
	"math/rand"
	"testing"

	"github.com/hoxca/exorciser/internal/detect"
)

type injected struct {
	x, y  int
	sigma float64
	amp   float64
}

func syntheticPlane(w, h int, ghosts []injected, noise float64, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	plane := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 0.5 + 0.00004*float64(x) + 0.00002*float64(y)
			v += rng.NormFloat64() * noise
			plane[y*w+x] = float32(v)
		}
	}
	for _, g := range ghosts {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dx := float64(x - g.x)
				dy := float64(y - g.y)
				plane[y*w+x] += float32(g.amp * math.Exp(-(dx*dx+dy*dy)/(2*g.sigma*g.sigma)))
			}
		}
	}
	return plane
}

func TestScanFindsGhost(t *testing.T) {
	const w, h = 512, 512
	plane := syntheticPlane(w, h, []injected{{x: 300, y: 200, sigma: 8, amp: 0.03}}, 0.01, 1)

	arts, err := detect.Scan(plane, w, h, detect.ScanOptions{
		Scales: []float64{4, 8, 16}, MinSNR: 6, Workers: 4,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(arts) == 0 {
		t.Fatal("Scan found no artefact")
	}

	top := arts[0]
	if math.Abs(float64(top.X-300)) > 5 || math.Abs(float64(top.Y-200)) > 5 {
		t.Errorf("top artefact at (%d,%d), want near (300,200)", top.X, top.Y)
	}
	if top.Scale != 8 {
		t.Errorf("scale = %v, want 8", top.Scale)
	}
	if top.SNR < 6 {
		t.Errorf("snr = %v, want at least 6", top.SNR)
	}
	if top.Amp <= 0 {
		t.Errorf("amp = %v, want positive", top.Amp)
	}
}

func TestScanWithProfile(t *testing.T) {
	const w, h = 512, 512
	const gx, gy = 300, 200
	plane := syntheticPlane(w, h, []injected{{x: gx, y: gy, sigma: 10, amp: 0.03}}, 0.01, 1)

	profile := gaussianProfile(16, 8)
	arts, err := detect.Scan(plane, w, h, detect.ScanOptions{
		Profile:       &profile,
		ProfileScales: []float64{0.75, 1, 1.25},
		MinSNR:        6,
		Workers:       4,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(arts) == 0 {
		t.Fatal("Scan found no artefact with the profile")
	}
	top := arts[0]
	if math.Abs(float64(top.X-gx)) > 6 || math.Abs(float64(top.Y-gy)) > 6 {
		t.Errorf("top artefact at (%d,%d), want near (%d,%d)", top.X, top.Y, gx, gy)
	}
	if top.Scale < 6 || top.Scale > 10 {
		t.Errorf("scale = %v, want a scaled-profile equivalent sigma", top.Scale)
	}
}

func TestScanMaxPeakRatioRejectsCompactSource(t *testing.T) {
	const w, h = 512, 512
	const (
		compactX, compactY = 120, 120
		smoothX, smoothY   = 360, 360
	)
	plane := syntheticPlane(w, h, []injected{
		{x: compactX, y: compactY, sigma: 1, amp: 0.2},
		{x: smoothX, y: smoothY, sigma: 10, amp: 0.03},
	}, 0.01, 2)

	profile := gaussianProfile(16, 8)
	run := func(smoothness float64) []detect.Artefact {
		t.Helper()
		arts, err := detect.Scan(plane, w, h, detect.ScanOptions{
			Profile:       &profile,
			ProfileScales: []float64{0.75, 1},
			MaxPeakRatio:  smoothness,
			MinSNR:        5,
			Workers:       4,
		})
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		return arts
	}
	near := func(arts []detect.Artefact, x, y, radius float64) bool {
		for _, a := range arts {
			if math.Hypot(float64(a.X)-x, float64(a.Y)-y) <= radius {
				return true
			}
		}
		return false
	}

	unfiltered := run(0)
	if !near(unfiltered, compactX, compactY, 10) {
		t.Fatalf("unfiltered scan did not find the compact source: %v", unfiltered)
	}
	if !near(unfiltered, smoothX, smoothY, 10) {
		t.Fatalf("unfiltered scan did not find the smooth source: %v", unfiltered)
	}

	filtered := run(15)
	if near(filtered, compactX, compactY, 10) {
		t.Errorf("smoothness filter kept the compact source: %v", filtered)
	}
	if !near(filtered, smoothX, smoothY, 10) {
		t.Errorf("smoothness filter rejected the smooth source: %v", filtered)
	}
}

func gaussianProfile(radius int, sigma float64) detect.Profile {
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
	return detect.Profile{Radius: radius, Kernel: kernel}
}

func TestScanFindsSeveralGhosts(t *testing.T) {
	const w, h = 512, 512
	plane := syntheticPlane(w, h, []injected{
		{x: 120, y: 140, sigma: 4, amp: 0.015},
		{x: 390, y: 370, sigma: 8, amp: 0.03},
	}, 0.005, 2)

	arts, err := detect.Scan(plane, w, h, detect.ScanOptions{
		Scales: []float64{4, 8, 16}, MinSNR: 6, Workers: 2,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	for _, want := range []struct {
		x, y int
	}{{120, 140}, {390, 370}} {
		found := false
		for _, a := range arts {
			if math.Abs(float64(a.X-want.x)) <= 6 && math.Abs(float64(a.Y-want.y)) <= 6 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no artefact near (%d,%d) in %v", want.x, want.y, arts)
		}
	}
}

func TestScanFindsNegativeGhost(t *testing.T) {
	const w, h = 512, 512
	plane := syntheticPlane(w, h, []injected{{x: 200, y: 300, sigma: 8, amp: -0.03}}, 0.01, 3)

	arts, err := detect.Scan(plane, w, h, detect.ScanOptions{
		Scales: []float64{4, 8, 16}, MinSNR: 6, Workers: 2,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(arts) == 0 {
		t.Fatal("Scan found no artefact")
	}
	if arts[0].SNR >= 0 || arts[0].Amp >= 0 {
		t.Errorf("top artefact = %+v, want negative snr and amp", arts[0])
	}
}

func TestScanPolarity(t *testing.T) {
	const w, h = 512, 512
	plane := syntheticPlane(w, h, []injected{
		{x: 140, y: 150, sigma: 8, amp: 0.03},
		{x: 370, y: 360, sigma: 8, amp: -0.03},
	}, 0.01, 11)

	base := detect.ScanOptions{
		Scales: []float64{4, 8, 16}, MinSNR: 6, Workers: 4,
	}

	tests := []struct {
		name     string
		polarity detect.Polarity
		wantSign int
		wantX    int
		wantY    int
	}{
		{name: "positive", polarity: detect.PolarityPositive, wantSign: 1, wantX: 140, wantY: 150},
		{name: "negative", polarity: detect.PolarityNegative, wantSign: -1, wantX: 370, wantY: 360},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := base
			opts.Polarity = tt.polarity
			arts, err := detect.Scan(plane, w, h, opts)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(arts) == 0 {
				t.Fatal("Scan found no artefact")
			}
			found := false
			for _, a := range arts {
				if sign(a.SNR) != tt.wantSign {
					t.Errorf("artefact %+v has the wrong sign for %s", a, tt.polarity)
				}
				if math.Abs(float64(a.X-tt.wantX)) <= 6 && math.Abs(float64(a.Y-tt.wantY)) <= 6 {
					found = true
				}
			}
			if !found {
				t.Errorf("no %s artefact near (%d,%d) in %v", tt.name, tt.wantX, tt.wantY, arts)
			}
		})
	}

	t.Run("both", func(t *testing.T) {
		arts, err := detect.Scan(plane, w, h, base)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		var positive, negative bool
		for _, a := range arts {
			switch sign(a.SNR) {
			case 1:
				positive = true
			case -1:
				negative = true
			}
		}
		if !positive || !negative {
			t.Errorf("both-polarity scan found positive=%v negative=%v in %v", positive, negative, arts)
		}
	})
}

func TestParsePolarity(t *testing.T) {
	tests := []struct {
		in   string
		want detect.Polarity
		ok   bool
	}{
		{in: "both", want: detect.PolarityBoth, ok: true},
		{in: "positive", want: detect.PolarityPositive, ok: true},
		{in: "negative", want: detect.PolarityNegative, ok: true},
		{in: " Positive ", want: detect.PolarityPositive, ok: true},
		{in: "", ok: false},
		{in: "bright", ok: false},
	}
	for _, tt := range tests {
		got, err := detect.ParsePolarity(tt.in)
		if tt.ok {
			if err != nil {
				t.Errorf("ParsePolarity(%q): %v", tt.in, err)
				continue
			}
			if got != tt.want {
				t.Errorf("ParsePolarity(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if got.String() != tt.want.String() {
				t.Errorf("Polarity(%q).String() = %q, want %q", tt.in, got.String(), tt.want.String())
			}
		} else if err == nil {
			t.Errorf("ParsePolarity(%q) = %v, want an error", tt.in, got)
		}
	}
}

func TestScanRejectsUnknownPolarity(t *testing.T) {
	const w, h = 64, 64
	plane := syntheticPlane(w, h, nil, 0.01, 12)
	_, err := detect.Scan(plane, w, h, detect.ScanOptions{
		Scales: []float64{4}, MinSNR: 6, Workers: 2, Polarity: detect.Polarity(99),
	})
	if err == nil {
		t.Fatal("Scan accepted an unknown polarity")
	}
}

func sign(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

func TestScanFindsStrongGhost(t *testing.T) {
	const w, h = 512, 512
	// Stacking raises a ghost by about sqrt(N); a bright ghost must not be
	// removed by the star mask.
	plane := syntheticPlane(w, h, []injected{{x: 250, y: 260, sigma: 8, amp: 0.2}}, 0.01, 5)

	arts, err := detect.Scan(plane, w, h, detect.ScanOptions{
		Scales: []float64{4, 8, 16}, MinSNR: 6, Workers: 4,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(arts) == 0 {
		t.Fatal("Scan found no artefact")
	}
	top := arts[0]
	if math.Abs(float64(top.X-250)) > 5 || math.Abs(float64(top.Y-260)) > 5 {
		t.Errorf("top artefact at (%d,%d), want near (250,260)", top.X, top.Y)
	}
}

func TestStarMask(t *testing.T) {
	const w, h = 512, 512
	plane := syntheticPlane(w, h, []injected{
		{x: 150, y: 150, sigma: 1.5, amp: 0.5}, // a star
		{x: 350, y: 350, sigma: 16, amp: 0.1},  // extended nebulosity
	}, 0.01, 7)

	mask, err := detect.StarMask(plane, w, h, detect.StarMaskOptions{
		Sigma: 5, Dilation: 8, Border: 16, Workers: 4,
	})
	if err != nil {
		t.Fatalf("StarMask: %v", err)
	}
	if !mask[150*w+150] {
		t.Error("star centre was not masked")
	}
	if mask[350*w+350] {
		t.Error("extended structure was masked as a star")
	}
	if !mask[0] || !mask[16*w+16] {
		t.Error("border was not masked")
	}
	if mask[100*w+300] {
		t.Error("blank sky was masked")
	}
}

func TestScanIgnoresNoise(t *testing.T) {
	const w, h = 512, 512
	plane := syntheticPlane(w, h, nil, 0.01, 4)

	arts, err := detect.Scan(plane, w, h, detect.ScanOptions{
		Scales: []float64{4, 8, 16}, MinSNR: 8, Workers: 4,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(arts) != 0 {
		t.Errorf("Scan found %d artefacts in pure noise: %v", len(arts), arts)
	}
}

func TestScanRejectsFlatImage(t *testing.T) {
	const w, h = 512, 512
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = 1
	}
	if _, err := detect.Scan(plane, w, h, detect.ScanOptions{}); err == nil {
		t.Fatal("Scan accepted a flat image with no noise")
	}
}

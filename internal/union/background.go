package union

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/hoxca/exorciser/internal/report"
)

// Background is a square of sky chosen as a representative background sample.
// X and Y are the window centre in registered pixels. Pixels is the number of
// samples that survived the star mask and clipping; Mean, Noise and SNR are
// computed from those samples only.
type Background struct {
	X      int
	Y      int
	Size   int
	Mean   float64
	Noise  float64
	SNR    float64
	Pixels int
}

const (
	// backgroundStep is the spacing, in pixels, between candidate centres. A
	// full-resolution search would cost far more for no practical gain.
	backgroundStep = 16
	// backgroundStride is the sampling step used to estimate the whole-image
	// sky level and noise.
	backgroundStride = 8
	// backgroundClipSigma and backgroundClipPasses define the sigma clipping
	// applied to the star-masked pixels of each candidate window.
	backgroundClipSigma  = 3.0
	backgroundClipPasses = 2
	// backgroundMinSurvivors is the fraction of a window that must survive
	// the mask and clipping for its statistics to be representative.
	backgroundMinSurvivors = 0.75
	// backgroundMinQuadrant is the fraction of each corner quadrant that must
	// survive, so no quadrant mean rests on too few pixels.
	backgroundMinQuadrant = 0.25
	// backgroundNoiseWeight is how much the window's level contributes beyond
	// uniformity. Uniformity is the primary requirement, so the level is only
	// a tie-breaker.
	backgroundNoiseWeight = 0.1
)

// FindBackground chooses a size×size window of uniform sky from plane.
//
// mask marks pixels to exclude, normally the stars found in the clean master.
// A nil mask excludes nothing. Within each candidate the unmasked pixels are
// sigma-clipped, so faint stars and cosmic rays that the mask missed do not
// enter the statistics. A candidate is only considered when enough pixels
// survive, both in total and in each of its four corner quadrants.
//
// Uniformity decides the winner: the spread between the four quadrant means,
// measured in whole-image noise sigmas, carries the full weight. The window's
// mean sky level enters with a small weight, so a flat patch is preferred to a
// brighter or darker one only when it is also representative. This rejects
// nebulosity, which raises the quadrant spread even when its average level
// happens to match the frame median.
//
// size must be odd so that the window has a centre pixel.
func FindBackground(plane []float32, w, h int, artefacts []report.Artefact, mask []bool, size int) (Background, error) {
	if w <= 0 || h <= 0 {
		return Background{}, fmt.Errorf("image is %d×%d, want positive dimensions", w, h)
	}
	if len(plane) < w*h {
		return Background{}, fmt.Errorf("image has %d samples, want at least %d", len(plane), w*h)
	}
	if mask != nil && len(mask) < w*h {
		return Background{}, fmt.Errorf("mask has %d pixels, want at least %d", len(mask), w*h)
	}
	if size < 3 || size%2 == 0 {
		return Background{}, fmt.Errorf("background window must be an odd number of pixels of at least 3, got %d", size)
	}
	if w < size || h < size {
		return Background{}, fmt.Errorf("%d×%d image is too small for a %d×%d window", w, h, size, size)
	}

	median, sigma, err := skyStats(plane, w, h, mask)
	if err != nil {
		return Background{}, err
	}

	half := size / 2
	discs := make([]disc, 0, len(artefacts))
	for _, a := range artefacts {
		r := 3 * a.Scale
		if r < 0 || math.IsNaN(r) || math.IsInf(r, 0) {
			r = 0
		}
		discs = append(discs, disc{x: float64(a.X), y: float64(a.Y), r: r})
	}

	total := size * size
	minSurvivors := int(math.Ceil(backgroundMinSurvivors * float64(total)))
	quadrantSize := half * half
	minQuadrant := int(math.Ceil(backgroundMinQuadrant * float64(quadrantSize)))

	idx := make([]int, 0, total)
	best := Background{Size: size}
	bestScore := math.Inf(1)
	bestNoise := math.Inf(1)
	found := false

	for cy := half; cy <= h-1-half; cy += backgroundStep {
		for cx := half; cx <= w-1-half; cx += backgroundStep {
			if excluded(cx, cy, half, discs) {
				continue
			}

			x0, y0 := cx-half, cy-half
			idx = idx[:0]
			for y := y0; y <= cy+half; y++ {
				line := y * w
				for x := x0; x <= cx+half; x++ {
					i := line + x
					if mask != nil && mask[i] {
						continue
					}
					v := float64(plane[i])
					if math.IsNaN(v) || math.IsInf(v, 0) {
						continue
					}
					idx = append(idx, i)
				}
			}
			if len(idx) < minSurvivors {
				continue
			}

			mean, noise := windowStats(plane, idx)
			for pass := 0; pass < backgroundClipPasses && noise > 0; pass++ {
				limit := backgroundClipSigma * noise
				kept := idx[:0]
				for _, i := range idx {
					if math.Abs(float64(plane[i])-mean) <= limit {
						kept = append(kept, i)
					}
				}
				idx = kept
				if len(idx) < minSurvivors {
					break
				}
				mean, noise = windowStats(plane, idx)
			}
			if len(idx) < minSurvivors {
				continue
			}

			var counts [4]int
			var sums [4]float64
			for _, i := range idx {
				lx := i%w - x0
				ly := i/w - y0
				q := -1
				switch {
				case ly <= half-1 && lx <= half-1:
					q = 0
				case ly <= half-1 && lx >= half+1:
					q = 1
				case ly >= half+1 && lx <= half-1:
					q = 2
				case ly >= half+1 && lx >= half+1:
					q = 3
				}
				if q >= 0 {
					counts[q]++
					sums[q] += float64(plane[i])
				}
			}
			representative := true
			for q := 0; q < 4; q++ {
				if counts[q] < minQuadrant {
					representative = false
					break
				}
			}
			if !representative {
				continue
			}

			qMin, qMax := math.Inf(1), math.Inf(-1)
			for q := 0; q < 4; q++ {
				qMean := sums[q] / float64(counts[q])
				qMin = math.Min(qMin, qMean)
				qMax = math.Max(qMax, qMean)
			}
			spread := qMax - qMin
			score := spread/sigma +
				backgroundNoiseWeight*math.Abs(mean-median)/sigma

			if !found || betterBackground(score, noise, cy, cx, bestScore, bestNoise, best) {
				found = true
				bestScore = score
				bestNoise = noise
				best.X = cx
				best.Y = cy
				best.Mean = mean
				best.Noise = noise
				best.Pixels = len(idx)
				if noise > 0 {
					best.SNR = mean / noise
				}
			}
		}
	}

	if !found {
		return Background{}, errors.New("no window of sky is free of artefacts with enough surviving pixels; review the catalogue or use a cleaner frame")
	}
	return best, nil
}

// windowStats returns the mean and population standard deviation of the
// samples of plane named by idx.
func windowStats(plane []float32, idx []int) (mean, noise float64) {
	if len(idx) == 0 {
		return 0, 0
	}
	var sum, sq float64
	for _, i := range idx {
		v := float64(plane[i])
		sum += v
		sq += v * v
	}
	n := float64(len(idx))
	mean = sum / n
	variance := sq/n - mean*mean
	if variance < 0 {
		variance = 0
	}
	return mean, math.Sqrt(variance)
}

type disc struct {
	x, y, r float64
}

// excluded reports whether the size×size window centred at (x, y) touches a
// clearance disc. A zero radius still excludes a window containing the
// artefact centre.
func excluded(x, y, half int, discs []disc) bool {
	x0, x1 := x-half, x+half
	y0, y1 := y-half, y+half
	for _, d := range discs {
		dx := 0.0
		switch {
		case d.x < float64(x0):
			dx = float64(x0) - d.x
		case d.x > float64(x1):
			dx = d.x - float64(x1)
		}
		dy := 0.0
		switch {
		case d.y < float64(y0):
			dy = float64(y0) - d.y
		case d.y > float64(y1):
			dy = d.y - float64(y1)
		}
		if dx*dx+dy*dy <= d.r*d.r {
			return true
		}
	}
	return false
}

// betterBackground orders candidates by score, then by noise, then by
// position. The position tie-break makes the choice deterministic.
func betterBackground(score, noise float64, y, x int, bestScore, bestNoise float64, best Background) bool {
	const epsilon = 1e-12
	if score < bestScore-epsilon {
		return true
	}
	if math.Abs(score-bestScore) > epsilon {
		return false
	}
	if noise < bestNoise-epsilon {
		return true
	}
	if math.Abs(noise-bestNoise) > epsilon {
		return false
	}
	if y != best.Y {
		return y < best.Y
	}
	return x < best.X
}

// skyStats estimates the whole-image sky level and noise from a strided
// sample, using a median and a MAD. Masked pixels are skipped, so stars do not
// bias the reference level or its noise.
func skyStats(plane []float32, w, h int, mask []bool) (median, sigma float64, err error) {
	samples := make([]float64, 0, (w/backgroundStride+1)*(h/backgroundStride+1))
	for y := 0; y < h; y += backgroundStride {
		line := y * w
		for x := 0; x < w; x += backgroundStride {
			i := line + x
			if mask != nil && mask[i] {
				continue
			}
			v := float64(plane[i])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			samples = append(samples, v)
		}
	}
	if len(samples) < 16 {
		return 0, 0, errors.New("too few finite pixels to estimate the sky background")
	}
	sort.Float64s(samples)
	median = samples[len(samples)/2]

	deviations := make([]float64, len(samples))
	for i, v := range samples {
		deviations[i] = math.Abs(v - median)
	}
	sort.Float64s(deviations)
	mad := deviations[len(deviations)/2]
	sigma = 1.4826 * mad
	if sigma <= 0 {
		return 0, 0, errors.New("sky background noise estimate is zero")
	}
	return median, sigma, nil
}

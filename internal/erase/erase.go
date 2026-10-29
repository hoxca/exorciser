// Package erase inpaints ghost artefacts by copying structure from a clean
// reference image and restoring the target's own sky level and noise.
package erase

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
)

const (
	// clipSigma and clipPasses define the sigma clipping used to measure a
	// window of sky without letting stars bias the statistics.
	clipSigma  = 3.0
	clipPasses = 2
)

// Stats describes a measured patch of sky.
type Stats struct {
	Mean   float64
	Noise  float64
	Pixels int
}

// WindowStats measures the sigma-clipped sky level and noise of a size×size
// window centred on (cx, cy). Non-finite pixels are skipped. The window must
// lie entirely inside the image and its side must be odd.
func WindowStats(plane []float32, w, h, cx, cy, size int) (Stats, error) {
	if w <= 0 || h <= 0 {
		return Stats{}, fmt.Errorf("image is %d×%d, want positive dimensions", w, h)
	}
	if len(plane) < w*h {
		return Stats{}, fmt.Errorf("image has %d samples, want at least %d", len(plane), w*h)
	}
	if size < 3 || size%2 == 0 {
		return Stats{}, fmt.Errorf("window must be an odd number of pixels of at least 3, got %d", size)
	}
	half := size / 2
	if cx-half < 0 || cx+half >= w || cy-half < 0 || cy+half >= h {
		return Stats{}, fmt.Errorf("window centred at (%d,%d) of size %d falls outside %d×%d",
			cx, cy, size, w, h)
	}

	idx := make([]int, 0, size*size)
	for y := cy - half; y <= cy+half; y++ {
		line := y * w
		for x := cx - half; x <= cx+half; x++ {
			i := line + x
			v := float64(plane[i])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			idx = append(idx, i)
		}
	}
	if len(idx) < 16 {
		return Stats{}, errors.New("too few finite pixels in the window")
	}

	mean, noise := stats(plane, idx)
	for pass := 0; pass < clipPasses && noise > 0; pass++ {
		limit := clipSigma * noise
		kept := idx[:0]
		for _, i := range idx {
			if math.Abs(float64(plane[i])-mean) <= limit {
				kept = append(kept, i)
			}
		}
		idx = kept
		if len(idx) < 16 {
			return Stats{}, errors.New("too few pixels survive clipping in the window")
		}
		mean, noise = stats(plane, idx)
	}
	if noise <= 0 {
		return Stats{}, errors.New("window has no noise")
	}
	return Stats{Mean: mean, Noise: noise, Pixels: len(idx)}, nil
}

// RingStats measures the sigma-clipped sky level and noise in an annulus
// around (cx, cy). It leaves the artefact itself out of the sample, so the
// local sky can be measured next to a repaired disc. The annulus must lie
// inside the image.
func RingStats(plane []float32, w, h, cx, cy, inner, outer int) (Stats, error) {
	if w <= 0 || h <= 0 {
		return Stats{}, fmt.Errorf("image is %d×%d, want positive dimensions", w, h)
	}
	if len(plane) < w*h {
		return Stats{}, fmt.Errorf("image has %d samples, want at least %d", len(plane), w*h)
	}
	if inner < 0 || outer <= inner {
		return Stats{}, fmt.Errorf("annulus radii must satisfy 0 <= inner < outer, got %d and %d",
			inner, outer)
	}
	if cx-outer < 0 || cx+outer >= w || cy-outer < 0 || cy+outer >= h {
		return Stats{}, fmt.Errorf("annulus centred at (%d,%d) of outer radius %d falls outside %d×%d",
			cx, cy, outer, w, h)
	}

	inner2 := inner * inner
	outer2 := outer * outer
	idx := make([]int, 0, (2*outer+1)*(2*outer+1))
	for y := cy - outer; y <= cy+outer; y++ {
		dy := y - cy
		line := y * w
		for x := cx - outer; x <= cx+outer; x++ {
			dx := x - cx
			d2 := dx*dx + dy*dy
			if d2 < inner2 || d2 > outer2 {
				continue
			}
			i := line + x
			v := float64(plane[i])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			idx = append(idx, i)
		}
	}
	if len(idx) < 16 {
		return Stats{}, errors.New("too few finite pixels in the annulus")
	}

	mean, noise := stats(plane, idx)
	for pass := 0; pass < clipPasses && noise > 0; pass++ {
		limit := clipSigma * noise
		kept := idx[:0]
		for _, i := range idx {
			if math.Abs(float64(plane[i])-mean) <= limit {
				kept = append(kept, i)
			}
		}
		idx = kept
		if len(idx) < 16 {
			return Stats{}, errors.New("too few pixels survive clipping in the annulus")
		}
		mean, noise = stats(plane, idx)
	}
	if noise <= 0 {
		return Stats{}, errors.New("annulus has no noise")
	}
	return Stats{Mean: mean, Noise: noise, Pixels: len(idx)}, nil
}

func stats(plane []float32, idx []int) (mean, noise float64) {
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

// Calibration maps the clean reference onto the target's sky level and noise.
// Target* describes the image being repaired; Clean* describes the clean
// reference, both measured in the same background window.
type Calibration struct {
	TargetMean  float64
	TargetNoise float64
	CleanMean   float64
	CleanNoise  float64
}

// Inpaint replaces the disc of the given radius centred on (cx, cy) with the
// matching part of clean. The clean samples are shifted onto the target's sky
// level and Gaussian noise is added to bring the patch up to the target's
// noise. A feather of the given width inside the radius blends the patch into
// the frame so no hard rim remains.
//
// rng supplies the added noise; callers seed it deterministically when the
// result must be reproducible.
func Inpaint(dst, clean []float32, w, h, cx, cy, radius, feather int, cal Calibration, rng *rand.Rand) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("image is %d×%d, want positive dimensions", w, h)
	}
	if len(dst) < w*h || len(clean) < w*h {
		return fmt.Errorf("planes must hold at least %d samples", w*h)
	}
	if radius < 1 {
		return fmt.Errorf("radius must be positive, got %d", radius)
	}
	if feather < 0 || feather > radius {
		return fmt.Errorf("feather must be between 0 and the radius %d, got %d", radius, feather)
	}
	if rng == nil {
		return errors.New("noise source is nil")
	}

	added := 0.0
	if cal.TargetNoise > cal.CleanNoise {
		added = math.Sqrt(cal.TargetNoise*cal.TargetNoise - cal.CleanNoise*cal.CleanNoise)
	}

	r2 := radius * radius
	inner := radius - feather
	featherF := float64(feather)
	for y := max(0, cy-radius); y <= min(h-1, cy+radius); y++ {
		line := y * w
		dy := y - cy
		for x := max(0, cx-radius); x <= min(w-1, cx+radius); x++ {
			dx := x - cx
			d2 := dx*dx + dy*dy
			if d2 > r2 {
				continue
			}

			weight := 1.0
			if feather > 0 {
				d := math.Sqrt(float64(d2))
				if d > float64(inner) {
					t := (d - float64(inner)) / featherF
					weight = 0.5 * (1 + math.Cos(math.Pi*t))
				}
			}
			if weight <= 0 {
				continue
			}

			i := line + x
			base := cal.TargetMean
			if v := float64(clean[i]); !math.IsNaN(v) && !math.IsInf(v, 0) {
				base += v - cal.CleanMean
			}
			value := base + rng.NormFloat64()*added

			original := float64(dst[i])
			dst[i] = float32((1-weight)*original + weight*value)
		}
	}
	return nil
}

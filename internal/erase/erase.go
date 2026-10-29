// Package erase inpaints ghost artefacts by copying structure from a clean
// reference image and restoring the target's own sky level and noise.
package erase

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
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

// Plane is a first-order sky model c0 + c1*dx + c2*dy around a local origin.
type Plane struct {
	C0, C1, C2 float64
}

// At evaluates the plane at an offset from its origin.
func (p Plane) At(dx, dy float64) float64 {
	return p.C0 + p.C1*dx + p.C2*dy
}

// LocalStats is a plane fit and its residual noise over an annulus.
type LocalStats struct {
	Plane  Plane
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
	return RingStatsMasked(plane, w, h, cx, cy, inner, outer, nil)
}

// RingStatsMasked is RingStats with full-image pixels marked in mask excluded.
// Passing a star mask keeps bright neighbours from inflating the local noise.
func RingStatsMasked(plane []float32, w, h, cx, cy, inner, outer int, mask []bool) (Stats, error) {
	if w <= 0 || h <= 0 {
		return Stats{}, fmt.Errorf("image is %d×%d, want positive dimensions", w, h)
	}
	if len(plane) < w*h {
		return Stats{}, fmt.Errorf("image has %d samples, want at least %d", len(plane), w*h)
	}
	if mask != nil && len(mask) < w*h {
		return Stats{}, errors.New("mask is smaller than the image")
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

// RingPlaneStats fits a first-order sky plane to an annulus, clipping outliers.
// The plane coefficients are relative to (cx, cy); its C0 is the fitted level
// at the artefact centre. Passing a star mask keeps bright neighbours out of
// the fit.
func RingPlaneStats(plane []float32, w, h, cx, cy, inner, outer int, mask []bool) (LocalStats, error) {
	if w <= 0 || h <= 0 {
		return LocalStats{}, fmt.Errorf("image is %d×%d, want positive dimensions", w, h)
	}
	if len(plane) < w*h {
		return LocalStats{}, fmt.Errorf("image has %d samples, want at least %d", len(plane), w*h)
	}
	if mask != nil && len(mask) < w*h {
		return LocalStats{}, errors.New("mask is smaller than the image")
	}
	if inner < 0 || outer <= inner {
		return LocalStats{}, fmt.Errorf("annulus radii must satisfy 0 <= inner < outer, got %d and %d",
			inner, outer)
	}
	if cx-outer < 0 || cx+outer >= w || cy-outer < 0 || cy+outer >= h {
		return LocalStats{}, fmt.Errorf("annulus centred at (%d,%d) of outer radius %d falls outside %d×%d",
			cx, cy, outer, w, h)
	}

	inner2 := inner * inner
	outer2 := outer * outer
	us := make([]float64, 0, (2*outer+1)*(2*outer+1))
	vs := make([]float64, 0, cap(us))
	zs := make([]float64, 0, cap(us))
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
			if mask != nil && mask[i] {
				continue
			}
			v := float64(plane[i])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			us = append(us, float64(dx))
			vs = append(vs, float64(dy))
			zs = append(zs, v)
		}
	}
	if len(zs) < 50 {
		return LocalStats{}, fmt.Errorf("only %d usable annulus pixels, need 50", len(zs))
	}

	keep := make([]bool, len(zs))
	for i := range keep {
		keep[i] = true
	}
	var p Plane
	var residuals []float64
	for iter := 0; iter < 3; iter++ {
		var err error
		p, err = solvePlane(us, vs, zs, keep)
		if err != nil {
			return LocalStats{}, err
		}
		residuals = residuals[:0]
		for i := range zs {
			if keep[i] {
				residuals = append(residuals, zs[i]-p.At(us[i], vs[i]))
			}
		}
		_, mad := robustSpread(residuals)
		if mad == 0 {
			break
		}
		limit := 3 * 1.4826 * mad
		for i := range zs {
			if keep[i] && math.Abs(zs[i]-p.At(us[i], vs[i])) > limit {
				keep[i] = false
			}
		}
	}
	residuals = residuals[:0]
	kept := 0
	for i := range zs {
		if keep[i] {
			residuals = append(residuals, zs[i]-p.At(us[i], vs[i]))
			kept++
		}
	}
	if kept < 50 {
		return LocalStats{}, fmt.Errorf("only %d annulus pixels survive clipping, need 50", kept)
	}
	_, mad := robustSpread(residuals)
	noise := 1.4826 * mad
	if noise <= 0 {
		return LocalStats{}, errors.New("annulus has no noise")
	}
	return LocalStats{Plane: p, Noise: noise, Pixels: kept}, nil
}

func solvePlane(us, vs, zs []float64, keep []bool) (Plane, error) {
	var n, su, sv, suu, svv, suv, sz, suz, svz float64
	for i := range zs {
		if !keep[i] {
			continue
		}
		u, v, z := us[i], vs[i], zs[i]
		n++
		su += u
		sv += v
		suu += u * u
		svv += v * v
		suv += u * v
		sz += z
		suz += u * z
		svz += v * z
	}
	if n < 10 {
		return Plane{}, errors.New("too few annulus pixels to fit a plane")
	}
	m := [3][3]float64{
		{n, su, sv},
		{su, suu, suv},
		{sv, suv, svv},
	}
	rhs := [3]float64{sz, suz, svz}
	det := determinant(m)
	if det == 0 || math.Abs(det) < 1e-12*n*n*n {
		return Plane{}, errors.New("annulus geometry is degenerate")
	}
	solve := func(col int) float64 {
		mm := m
		for row := 0; row < 3; row++ {
			mm[row][col] = rhs[row]
		}
		return determinant(mm) / det
	}
	return Plane{C0: solve(0), C1: solve(1), C2: solve(2)}, nil
}

func determinant(m [3][3]float64) float64 {
	return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
}

func robustSpread(vals []float64) (float64, float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	med := s[len(s)/2]
	dev := make([]float64, len(s))
	for i, v := range s {
		dev[i] = math.Abs(v - med)
	}
	sort.Float64s(dev)
	return med, dev[len(dev)/2]
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
	// TargetPlane and CleanPlane, when set, replace the scalar means with a
	// local first-order sky model. Scale maps the clean structure onto the
	// target's flux scale.
	TargetPlane *Plane
	CleanPlane  *Plane
	Scale       float64
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

	scale := cal.Scale
	if scale == 0 {
		scale = 1
	}
	cleanNoise := math.Abs(cal.CleanNoise * scale)
	added := 0.0
	if cal.TargetNoise > cleanNoise {
		added = math.Sqrt(cal.TargetNoise*cal.TargetNoise - cleanNoise*cleanNoise)
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
			dxf, dyf := float64(dx), float64(dy)
			targetLevel := cal.TargetMean
			if cal.TargetPlane != nil {
				targetLevel = cal.TargetPlane.At(dxf, dyf)
			}
			cleanLevel := cal.CleanMean
			if cal.CleanPlane != nil {
				cleanLevel = cal.CleanPlane.At(dxf, dyf)
			}
			base := targetLevel
			if v := float64(clean[i]); !math.IsNaN(v) && !math.IsInf(v, 0) {
				base += scale * (v - cleanLevel)
			}
			value := base + rng.NormFloat64()*added

			original := float64(dst[i])
			dst[i] = float32((1-weight)*original + weight*value)
		}
	}
	return nil
}

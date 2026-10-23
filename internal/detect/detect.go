// Package detect measures how significant a brighter diffuse feature in a
// window of a frame is, using another frame of the same field as a reference.
// Darker features are ignored: a frame is only ever detected when it is
// brighter than the reference.
//
// The two frames are related by an affine transform, because the pixel data is
// linear: frame ≈ A*ref + B. After removing that transform and a local
// background plane, the residual inside the window is matched-filtered with a
// Gaussian kernel. Per-pixel noise is far larger than a faint ghost, but the
// filter integrates over the ghost's area, turning an invisible per-pixel
// signal into a measurable signal-to-noise ratio.
package detect

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// Affine is the least-squares relation frame ≈ A*ref + B.
type Affine struct {
	A float64
	B float64
}

// Options configures a measurement.
type Options struct {
	// Window is the region of interest, validated against the image.
	Window Window
	// Margin is the width in pixels of the background ring around the window.
	// It must exceed the largest kernel radius.
	Margin int
	// Scales are the Gaussian sigmas, in pixels, tried by the matched filter.
	Scales []float64
	// Stride is the subsampling step used to fit the affine transform.
	Stride int
	// ClipSigma masks residual pixels beyond this many noise sigmas.
	ClipSigma float64
}

// Result describes the strongest brighter-than-reference feature found in the
// window. When no positive response exists the peak fields and PeakSNR are
// zero.
type Result struct {
	Affine  Affine
	Noise   float64
	Masked  int
	Scale   float64
	PeakX   int
	PeakY   int
	PeakSNR float64
	PeakAmp float64
}

func (o Options) withDefaults() Options {
	if o.Margin == 0 {
		o.Margin = 64
	}
	if len(o.Scales) == 0 {
		o.Scales = []float64{4, 8, 16}
	}
	if o.Stride == 0 {
		o.Stride = 8
	}
	if o.ClipSigma == 0 {
		o.ClipSigma = 4
	}
	return o
}

// MaxKernelRadius is the radius of the widest matched-filter kernel the options
// require.
func MaxKernelRadius(scales []float64) int {
	r := 0
	for _, s := range scales {
		r = max(r, int(math.Ceil(3*s)))
	}
	return r
}

// fitBlock is the number of subsample points per side whose median represents
// one regression point. Four points (16 pixels) keep the median robust while
// still giving thousands of points on a full frame.
const fitBlock = 4

// minFitBlocks is the smallest usable regression set. Two parameters need far
// fewer, but a handful of blocks could be dominated by a single star field.
const minFitBlocks = 30

// Fit estimates the affine transform between frame and reference.
//
// It regresses block medians rather than individual pixels. Pixel-wise
// clipping would remove exactly the high-variance samples that constrain the
// slope — stars and nebulosity misaligned by a fraction of a pixel — and leave
// the fit chasing noise, which drives the slope towards zero. A median over a
// small block ignores those outliers locally while keeping the block's sky
// level, so the slope stays stable.
func Fit(frame, ref []float32, w, h, stride int) (Affine, error) {
	if w <= 0 || h <= 0 {
		return Affine{}, errors.New("image has no pixels")
	}
	if len(frame) < w*h || len(ref) < w*h {
		return Affine{}, errors.New("pixel buffers are smaller than the image")
	}
	if stride < 1 {
		stride = 1
	}

	fs, rs := blockMedians(frame, ref, w, h, stride)
	if len(fs) < minFitBlocks {
		return Affine{}, fmt.Errorf("only %d usable blocks to fit the reference, need %d",
			len(fs), minFitBlocks)
	}

	_, fmad := robustScale(fs)
	_, rmad := robustScale(rs)
	if fmad == 0 || rmad == 0 {
		return Affine{}, errors.New("cannot normalise a flat frame or reference")
	}

	all := make([]bool, len(fs))
	for i := range all {
		all[i] = true
	}
	aff, ok := fitLine(fs, rs, all)
	if !ok {
		return Affine{}, errors.New("reference has no variance to fit against")
	}
	if !finite(aff.A) || aff.A == 0 {
		return Affine{}, fmt.Errorf("reference normalisation gave a slope of %v", aff.A)
	}

	// One generous outlier pass. Block medians already reject stars, so the
	// remaining outliers are wide defects such as a trail covering most of a
	// block. The clip must stay loose: the blocks that constrain the slope are
	// the bright ones (nebulosity, star clusters), and a threshold near the
	// residual MAD would delete them and leave the fit chasing noise, which
	// pulls the slope towards zero.
	residuals := make([]float64, len(fs))
	for i := range fs {
		residuals[i] = fs[i] - (aff.A*rs[i] + aff.B)
	}
	_, mad := robustScale(residuals)
	limit := 10 * rms(residuals)
	if floor := 8 * 1.4826 * mad; floor > limit {
		limit = floor
	}

	keep := make([]bool, len(fs))
	kept := 0
	for i := range fs {
		keep[i] = math.Abs(residuals[i]) <= limit
		if keep[i] {
			kept++
		}
	}
	if kept < minFitBlocks {
		// The distribution is too heavy-tailed to clip safely; keep every block
		// rather than fit a biased subset.
		return aff, nil
	}
	if next, ok := fitLine(fs, rs, keep); ok {
		aff = next
	}
	return aff, nil
}

// rms returns the root mean square of vals.
func rms(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(vals)))
}

// blockMedians reduces a frame/reference pair to medians over fitBlock×fitBlock
// subsample points.
func blockMedians(frame, ref []float32, w, h, stride int) (fs, rs []float64) {
	gridW := (w + stride - 1) / stride
	gridH := (h + stride - 1) / stride

	fbuf := make([]float64, 0, fitBlock*fitBlock)
	rbuf := make([]float64, 0, fitBlock*fitBlock)
	for by := 0; by < gridH; by += fitBlock {
		for bx := 0; bx < gridW; bx += fitBlock {
			fbuf, rbuf = fbuf[:0], rbuf[:0]
			for j := by; j < by+fitBlock && j < gridH; j++ {
				line := (j * stride) * w
				for i := bx; i < bx+fitBlock && i < gridW; i++ {
					x := i * stride
					f := float64(frame[line+x])
					r := float64(ref[line+x])
					if !finite(f) || !finite(r) {
						continue
					}
					fbuf = append(fbuf, f)
					rbuf = append(rbuf, r)
				}
			}
			if len(fbuf) < fitBlock {
				continue
			}
			fmed, _ := robustScale(fbuf)
			rmed, _ := robustScale(rbuf)
			fs = append(fs, fmed)
			rs = append(rs, rmed)
		}
	}
	return fs, rs
}

// fitLine is an ordinary least-squares fit of f = a*r + b over the kept pairs.
func fitLine(fs, rs []float64, keep []bool) (Affine, bool) {
	var n, sumR, sumF float64
	for i := range fs {
		if !keep[i] {
			continue
		}
		n++
		sumR += rs[i]
		sumF += fs[i]
	}
	if n < 2 {
		return Affine{}, false
	}
	meanR, meanF := sumR/n, sumF/n

	var sxx, sxy float64
	for i := range fs {
		if !keep[i] {
			continue
		}
		dr := rs[i] - meanR
		sxx += dr * dr
		sxy += dr * (fs[i] - meanF)
	}
	if sxx == 0 {
		return Affine{}, false
	}
	a := sxy / sxx
	return Affine{A: a, B: meanF - a*meanR}, true
}

// Score fits the affine transform, removes the local background and returns the
// strongest positive matched-filter response in the window. A feature darker
// than the reference has a negative response and is never selected, so PeakSNR
// and PeakAmp are non-negative.
func Score(frame, ref []float32, w, h int, opts Options) (Result, error) {
	opts = opts.withDefaults()

	win := opts.Window
	if err := win.Validate(w, h); err != nil {
		return Result{}, err
	}
	for _, s := range opts.Scales {
		if s <= 0 || !finite(s) {
			return Result{}, fmt.Errorf("ghost scale must be positive, got %v", s)
		}
	}
	maxRadius := MaxKernelRadius(opts.Scales)
	if opts.Margin <= maxRadius {
		return Result{}, fmt.Errorf("margin %d must exceed the kernel radius %d", opts.Margin, maxRadius)
	}

	aff, err := Fit(frame, ref, w, h, opts.Stride)
	if err != nil {
		return Result{}, err
	}

	cx := win.X + win.Size/2
	cy := win.Y + win.Size/2
	half := win.Size/2 + maxRadius
	x0, y0 := cx-half, cy-half
	ext := 2 * half
	if x0 < 0 || y0 < 0 || x0+ext > w || y0+ext > h {
		return Result{}, fmt.Errorf("window plus a %d px kernel radius does not fit a %d×%d image",
			maxRadius, w, h)
	}

	// Background ring: the outer square minus the kernel support square.
	ox0, oy0 := win.X-opts.Margin, win.Y-opts.Margin
	ox1, oy1 := win.X+win.Size+opts.Margin, win.Y+win.Size+opts.Margin
	if ox0 < 0 || oy0 < 0 || ox1 > w || oy1 > h {
		return Result{}, fmt.Errorf("window plus a %d px margin does not fit a %d×%d image",
			opts.Margin, w, h)
	}

	us := make([]float64, 0, (ox1-ox0)*(oy1-oy0))
	vs := make([]float64, 0, cap(us))
	zs := make([]float64, 0, cap(us))
	for y := oy0; y < oy1; y++ {
		insideY := y >= y0 && y < y0+ext
		line := y * w
		for x := ox0; x < ox1; x++ {
			if insideY && x >= x0 && x < x0+ext {
				continue
			}
			f := float64(frame[line+x])
			r := float64(ref[line+x])
			if !finite(f) || !finite(r) {
				continue
			}
			us = append(us, float64(x-cx))
			vs = append(vs, float64(y-cy))
			zs = append(zs, f-(aff.A*r+aff.B))
		}
	}
	if len(zs) < 100 {
		return Result{}, fmt.Errorf("only %d background pixels in the margin, need 100", len(zs))
	}

	bg, noise, err := fitBackground(us, vs, zs)
	if err != nil {
		return Result{}, err
	}
	if noise <= 0 {
		return Result{}, errors.New("background noise estimate is zero")
	}

	// Residual over the kernel support square, with outliers masked.
	res := make([]float64, ext*ext)
	valid := make([]float64, ext*ext)
	masked := 0
	limit := opts.ClipSigma * noise
	for j := 0; j < ext; j++ {
		y := y0 + j
		line := y * w
		base := j * ext
		for i := 0; i < ext; i++ {
			x := x0 + i
			f := float64(frame[line+x])
			r := float64(ref[line+x])
			if !finite(f) || !finite(r) {
				masked++
				continue
			}
			v := f - (aff.A*r + aff.B) - bg.at(x, y, cx, cy)
			if math.Abs(v) > limit {
				masked++
				continue
			}
			res[base+i] = v
			valid[base+i] = 1
		}
	}
	dilateInvalid(valid, ext, maskDilation)
	// The dilated ring must not feed the filter: its weight is excluded from the
	// noise normalisation, so its samples must be excluded from the signal too.
	for i := range res {
		if valid[i] == 0 {
			res[i] = 0
		}
	}

	result := Result{Affine: aff, Noise: noise, Masked: masked}
	best := 0.0
	searched := 0
	for _, scale := range opts.Scales {
		radius := int(math.Ceil(3 * scale))
		kernel := gaussianWeights(radius, scale)
		wFull := 0.0
		for _, k := range kernel {
			wFull += k
		}
		wFull *= wFull

		signal := convSep(res, ext, kernel)
		weight := convSep(valid, ext, kernel)
		noiseSq := convSep(valid, ext, squared(kernel))

		for j := maxRadius; j < maxRadius+win.Size; j++ {
			for i := maxRadius; i < maxRadius+win.Size; i++ {
				idx := j*ext + i
				if weight[idx] <= 0.5*wFull {
					continue
				}
				searched++
				snr := signal[idx] / (noise * math.Sqrt(noiseSq[idx]))
				// Only brighter-than-reference features count. best starts at
				// zero, so a dark response is rejected here.
				if snr <= best {
					continue
				}
				best = snr
				result.Scale = scale
				result.PeakSNR = snr
				result.PeakAmp = signal[idx] / weight[idx]
				result.PeakX = win.X + i - maxRadius
				result.PeakY = win.Y + j - maxRadius
			}
		}
	}
	if searched == 0 {
		return Result{}, errors.New("window is entirely masked")
	}
	return result, nil
}

// plane is a first-order background: c0 + c1*u + c2*v around a local origin.
type plane struct {
	c0, c1, c2 float64
}

func (p plane) at(x, y, cx, cy int) float64 {
	return p.c0 + p.c1*float64(x-cx) + p.c2*float64(y-cy)
}

// fitBackground fits a plane to the ring samples, clipping outliers twice, and
// returns the plane and the robust residual noise.
func fitBackground(us, vs, zs []float64) (plane, float64, error) {
	keep := make([]bool, len(zs))
	for i := range keep {
		keep[i] = true
	}

	var p plane
	residuals := make([]float64, 0, len(zs))
	for iter := 0; iter < 3; iter++ {
		var err error
		p, err = solvePlane(us, vs, zs, keep)
		if err != nil {
			return plane{}, 0, err
		}

		residuals = residuals[:0]
		for i := range zs {
			if keep[i] {
				residuals = append(residuals, zs[i]-p.c0-p.c1*us[i]-p.c2*vs[i])
			}
		}
		_, mad := robustScale(residuals)
		if mad == 0 {
			break
		}
		limit := 3 * 1.4826 * mad
		for i := range zs {
			if keep[i] && math.Abs(zs[i]-p.c0-p.c1*us[i]-p.c2*vs[i]) > limit {
				keep[i] = false
			}
		}
	}

	residuals = residuals[:0]
	for i := range zs {
		if keep[i] {
			residuals = append(residuals, zs[i]-p.c0-p.c1*us[i]-p.c2*vs[i])
		}
	}
	if len(residuals) < 50 {
		return plane{}, 0, fmt.Errorf("only %d background pixels left after clipping, need 50", len(residuals))
	}
	_, mad := robustScale(residuals)
	return p, 1.4826 * mad, nil
}

// solvePlane fits c0 + c1*u + c2*v to the kept samples.
func solvePlane(us, vs, zs []float64, keep []bool) (plane, error) {
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
		return plane{}, errors.New("too few background pixels to fit a plane")
	}

	// Normal equations, solved by Cramer's rule on the symmetric 3x3 matrix.
	m := [3][3]float64{
		{n, su, sv},
		{su, suu, suv},
		{sv, suv, svv},
	}
	rhs := [3]float64{sz, suz, svz}
	det := determinant(m)
	if det == 0 || math.Abs(det) < 1e-12*n*n*n {
		return plane{}, errors.New("background geometry is degenerate")
	}

	solve := func(col int) float64 {
		mm := m
		for row := 0; row < 3; row++ {
			mm[row][col] = rhs[row]
		}
		return determinant(mm) / det
	}
	return plane{c0: solve(0), c1: solve(1), c2: solve(2)}, nil
}

func determinant(m [3][3]float64) float64 {
	return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
}

// gaussianWeights returns an unnormalised separable Gaussian kernel. Both the
// signal and the noise use the same kernel, so the normalisation cancels in the
// ratio.
func gaussianWeights(radius int, sigma float64) []float64 {
	weights := make([]float64, 2*radius+1)
	for i := -radius; i <= radius; i++ {
		weights[i+radius] = math.Exp(-float64(i*i) / (2 * sigma * sigma))
	}
	return weights
}

func squared(k []float64) []float64 {
	out := make([]float64, len(k))
	for i, v := range k {
		out[i] = v * v
	}
	return out
}

// convSep convolves src, an e×e square, with a separable kernel. Pixels outside
// the square are treated as zero; the kernels used here never reach the window
// interior from outside the support square.
func convSep(src []float64, e int, kernel []float64) []float64 {
	return convolvePlane(src, e, e, kernel, 1)
}

// dilateInvalid clears valid pixels within r pixels of an invalid one, so a
// masked star does not bleed through the kernel.
func dilateInvalid(valid []float64, e, r int) {
	dilateInvalidPlane(valid, e, e, r)
}

// maskDilation is how far, in pixels, a masked pixel spreads to its neighbours.
// The clip leaves the wings of a compact outlier intact, and those wings can
// carry enough integrated flux to imitate a diffuse feature; widening the hole
// removes the wings without touching a feature broader than the kernel.
const maskDilation = 4

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// robustScale returns the median and the unscaled median absolute deviation.
func robustScale(vals []float64) (median, mad float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	s := slices.Clone(vals)
	slices.Sort(s)
	median = medianOfSorted(s)
	for i, v := range s {
		s[i] = math.Abs(v - median)
	}
	slices.Sort(s)
	return median, medianOfSorted(s)
}

func medianOfSorted(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

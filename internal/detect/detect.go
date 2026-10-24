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

// Kernel names reported by Result.Kernel.
const (
	// KernelGaussian is a Gaussian matched filter from Options.Scales.
	KernelGaussian = "gaussian"
	// KernelProfile is the ghost profile from Options.Profile.
	KernelProfile = "profile"
)

// Options configures a measurement.
type Options struct {
	// Window is the region of interest, validated against the image.
	Window Window
	// Margin is the width in pixels of the background ring around the window.
	// It must exceed the largest kernel radius.
	Margin int
	// Scales are the Gaussian sigmas, in pixels, tried by the matched filter.
	Scales []float64
	// Profile is an optional ghost shape extracted from a dirty frame. When
	// set, it is tried in addition to Scales and the strongest response wins.
	// A nil Profile disables profile scoring.
	Profile *Profile
	// SearchRadius bounds the peak search to this many pixels around the
	// window centre. Zero searches the whole window.
	SearchRadius int
	// Stride is the subsampling step used to fit the affine transform.
	Stride int
	// ClipSigma masks residual pixels beyond this many noise sigmas.
	ClipSigma float64
	// ProfileClipSigma is the clipping threshold for the profile residual. It
	// is looser than ClipSigma because the profile is matched to the ghost's
	// own shape, and a compact ghost's core can exceed the Gaussian clip. The
	// Gaussian scales keep the stricter clip so a star's wings do not dominate.
	ProfileClipSigma float64
}

// Result describes the strongest brighter-than-reference feature found in the
// window. When no positive response exists the peak fields and PeakSNR are
// zero.
type Result struct {
	Affine  Affine
	Noise   float64
	Masked  int
	Kernel  string
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
	if o.ProfileClipSigma == 0 {
		o.ProfileClipSigma = 10
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

// prepared is the residual plane after the affine transform, background
// removal, clipping and mask dilation. Its res and valid slices form a square
// of side ext, centred on the window.
type prepared struct {
	affine Affine
	noise  float64
	res    []float64
	valid  []float64
	ext    int
	masked int
}

// ring is the geometry of the background annulus around a window: the outer
// square is the window expanded by the margin, and the inner square is the
// kernel support.
type ring struct {
	cx, cy             int
	x0, y0, ext        int
	ox0, oy0, ox1, oy1 int
}

func ringFor(win Window, kernelRadius, margin, w, h int) (ring, error) {
	if err := win.Validate(w, h); err != nil {
		return ring{}, err
	}
	if kernelRadius < 0 {
		return ring{}, fmt.Errorf("kernel radius must not be negative, got %d", kernelRadius)
	}
	if margin <= kernelRadius {
		return ring{}, fmt.Errorf("margin %d must exceed the kernel radius %d", margin, kernelRadius)
	}

	cx := win.X + win.Size/2
	cy := win.Y + win.Size/2
	half := win.Size/2 + kernelRadius
	x0, y0 := cx-half, cy-half
	ext := 2 * half
	if x0 < 0 || y0 < 0 || x0+ext > w || y0+ext > h {
		return ring{}, fmt.Errorf("window plus a %d px kernel radius does not fit a %d×%d image",
			kernelRadius, w, h)
	}

	ox0, oy0 := win.X-margin, win.Y-margin
	ox1, oy1 := win.X+win.Size+margin, win.Y+win.Size+margin
	if ox0 < 0 || oy0 < 0 || ox1 > w || oy1 > h {
		return ring{}, fmt.Errorf("window plus a %d px margin does not fit a %d×%d image",
			margin, w, h)
	}
	return ring{
		cx: cx, cy: cy, x0: x0, y0: y0, ext: ext,
		ox0: ox0, oy0: oy0, ox1: ox1, oy1: oy1,
	}, nil
}

// collectRing samples the margin annulus, skipping the kernel support square.
// Coordinates are relative to the window centre.
func collectRing(w int, g ring, sample func(i int) (float64, bool)) (us, vs, zs []float64, err error) {
	us = make([]float64, 0, (g.ox1-g.ox0)*(g.oy1-g.oy0))
	vs = make([]float64, 0, cap(us))
	zs = make([]float64, 0, cap(us))
	for y := g.oy0; y < g.oy1; y++ {
		insideY := y >= g.y0 && y < g.y0+g.ext
		line := y * w
		for x := g.ox0; x < g.ox1; x++ {
			if insideY && x >= g.x0 && x < g.x0+g.ext {
				continue
			}
			z, ok := sample(line + x)
			if !ok {
				continue
			}
			us = append(us, float64(x-g.cx))
			vs = append(vs, float64(y-g.cy))
			zs = append(zs, z)
		}
	}
	if len(zs) < 100 {
		return nil, nil, nil, fmt.Errorf("only %d background pixels in the margin, need 100", len(zs))
	}
	return us, vs, zs, nil
}

// prepare fits the affine transform and background, then builds the clipped,
// masked residual over a square that extends kernelRadius pixels beyond win.
// The background ring is the margin-wide border outside that square.
func prepare(frame, ref []float32, w, h int, win Window, kernelRadius, margin, stride int, clipSigma float64) (prepared, error) {
	if clipSigma <= 0 || !finite(clipSigma) {
		return prepared{}, fmt.Errorf("clip sigma must be positive, got %v", clipSigma)
	}

	g, err := ringFor(win, kernelRadius, margin, w, h)
	if err != nil {
		return prepared{}, err
	}

	aff, err := Fit(frame, ref, w, h, stride)
	if err != nil {
		return prepared{}, err
	}

	us, vs, zs, err := collectRing(w, g, func(i int) (float64, bool) {
		f := float64(frame[i])
		r := float64(ref[i])
		if !finite(f) || !finite(r) {
			return 0, false
		}
		return f - (aff.A*r + aff.B), true
	})
	if err != nil {
		return prepared{}, err
	}

	bg, noise, err := fitBackground(us, vs, zs)
	if err != nil {
		return prepared{}, err
	}
	if noise <= 0 {
		return prepared{}, errors.New("background noise estimate is zero")
	}

	// Residual over the kernel support square, with outliers masked.
	res := make([]float64, g.ext*g.ext)
	valid := make([]float64, g.ext*g.ext)
	masked := 0
	limit := clipSigma * noise
	for j := 0; j < g.ext; j++ {
		y := g.y0 + j
		line := y * w
		base := j * g.ext
		for i := 0; i < g.ext; i++ {
			x := g.x0 + i
			f := float64(frame[line+x])
			r := float64(ref[line+x])
			if !finite(f) || !finite(r) {
				masked++
				continue
			}
			v := f - (aff.A*r + aff.B) - bg.at(x, y, g.cx, g.cy)
			if math.Abs(v) > limit {
				masked++
				continue
			}
			res[base+i] = v
			valid[base+i] = 1
		}
	}
	dilateInvalid(valid, g.ext, maskDilation)
	// The dilated ring must not feed the filter: its weight is excluded from the
	// noise normalisation, so its samples must be excluded from the signal too.
	for i := range res {
		if valid[i] == 0 {
			res[i] = 0
		}
	}

	return prepared{affine: aff, noise: noise, res: res, valid: valid, ext: g.ext, masked: masked}, nil
}

// BackgroundNoise estimates the robust background noise of frame in the margin
// ring around win. Unlike Score it does not subtract a reference frame; it is
// used to report the reference frame's own noise.
func BackgroundNoise(frame []float32, w, h int, win Window, kernelRadius, margin int) (float64, error) {
	g, err := ringFor(win, kernelRadius, margin, w, h)
	if err != nil {
		return 0, err
	}
	us, vs, zs, err := collectRing(w, g, func(i int) (float64, bool) {
		f := float64(frame[i])
		if !finite(f) {
			return 0, false
		}
		return f, true
	})
	if err != nil {
		return 0, err
	}
	_, noise, err := fitBackground(us, vs, zs)
	if err != nil {
		return 0, err
	}
	if noise <= 0 {
		return 0, errors.New("background noise estimate is zero")
	}
	return noise, nil
}

// Score fits the affine transform, removes the local background and returns the
// strongest positive matched-filter response in the window. A feature darker
// than the reference has a negative response and is never selected, so PeakSNR
// and PeakAmp are non-negative.
//
// Score tries every Gaussian scale and, when Options.Profile is set, the ghost
// profile as well. Result.Kernel names the winner.
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
	if opts.SearchRadius < 0 {
		return Result{}, fmt.Errorf("search radius must not be negative, got %d", opts.SearchRadius)
	}
	if opts.ProfileClipSigma <= 0 || !finite(opts.ProfileClipSigma) {
		return Result{}, fmt.Errorf("profile clip sigma must be positive, got %v", opts.ProfileClipSigma)
	}

	maxRadius := MaxKernelRadius(opts.Scales)
	if opts.Profile != nil {
		if err := opts.Profile.validate(); err != nil {
			return Result{}, err
		}
		maxRadius = max(maxRadius, opts.Profile.Radius)
	}
	if opts.Margin <= maxRadius {
		return Result{}, fmt.Errorf("margin %d must exceed the kernel radius %d", opts.Margin, maxRadius)
	}

	prep, err := prepare(frame, ref, w, h, win, maxRadius, opts.Margin, opts.Stride, opts.ClipSigma)
	if err != nil {
		return Result{}, err
	}

	result := Result{Affine: prep.affine, Noise: prep.noise, Masked: prep.masked}
	best := 0.0
	searched := 0
	lo, hi := searchBounds(win.Size, maxRadius, opts.SearchRadius)

	for _, scale := range opts.Scales {
		radius := int(math.Ceil(3 * scale))
		kernel := gaussianWeights(radius, scale)
		wFull := 0.0
		for _, k := range kernel {
			wFull += k
		}
		wFull *= wFull

		signal := convSep(prep.res, prep.ext, kernel)
		weight := convSep(prep.valid, prep.ext, kernel)
		noiseSq := convSep(prep.valid, prep.ext, squared(kernel))

		for j := lo; j < hi; j++ {
			for i := lo; i < hi; i++ {
				idx := j*prep.ext + i
				if weight[idx] <= 0.5*wFull {
					continue
				}
				searched++
				snr := signal[idx] / (prep.noise * math.Sqrt(noiseSq[idx]))
				// Only brighter-than-reference features count. best starts at
				// zero, so a dark response is rejected here.
				if snr <= best {
					continue
				}
				best = snr
				result.Kernel = KernelGaussian
				result.Scale = scale
				result.PeakSNR = snr
				result.PeakAmp = signal[idx] / weight[idx]
				result.PeakX = win.X + i - maxRadius
				result.PeakY = win.Y + j - maxRadius
			}
		}
	}

	if opts.Profile != nil {
		profilePrep := prep
		if opts.ProfileClipSigma != opts.ClipSigma {
			profilePrep, err = prepare(frame, ref, w, h, win, maxRadius, opts.Margin, opts.Stride, opts.ProfileClipSigma)
			if err != nil {
				return Result{}, err
			}
		}

		kernel := opts.Profile.normalized()
		radius := opts.Profile.Radius
		// The profile is normalised to sum one, so a full-weight kernel has
		// wFull = 1.
		const wFull = 1.0

		signal := conv2D(profilePrep.res, profilePrep.ext, kernel, radius)
		weight := conv2D(profilePrep.valid, profilePrep.ext, kernel, radius)
		noiseSq := conv2D(profilePrep.valid, profilePrep.ext, squared(kernel), radius)

		for j := lo; j < hi; j++ {
			for i := lo; i < hi; i++ {
				idx := j*profilePrep.ext + i
				if weight[idx] <= 0.5*wFull {
					continue
				}
				searched++
				snr := signal[idx] / (profilePrep.noise * math.Sqrt(noiseSq[idx]))
				if snr <= best {
					continue
				}
				best = snr
				result.Kernel = KernelProfile
				result.Scale = 0
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

// searchBounds returns the support-plane index range searched for a peak. The
// window occupies [offset, offset+size); a positive searchRadius narrows that
// to a square around its middle.
func searchBounds(size, offset, searchRadius int) (lo, hi int) {
	lo, hi = offset, offset+size
	if searchRadius <= 0 {
		return lo, hi
	}
	centre := offset + size/2
	lo = max(lo, centre-searchRadius)
	hi = min(hi, centre+searchRadius+1)
	return lo, hi
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

// conv2D convolves src, an e×e square, with a non-separable (2r+1)² kernel
// centred on its middle element. Pixels outside the square are treated as zero,
// matching convSep.
func conv2D(src []float64, e int, kernel []float64, r int) []float64 {
	side := 2*r + 1
	out := make([]float64, e*e)
	for y := 0; y < e; y++ {
		for x := 0; x < e; x++ {
			var sum float64
			y0, y1 := max(0, y-r), min(e-1, y+r)
			x0, x1 := max(0, x-r), min(e-1, x+r)
			for yy := y0; yy <= y1; yy++ {
				line := yy * e
				kline := (yy - y + r) * side
				for xx := x0; xx <= x1; xx++ {
					sum += src[line+xx] * kernel[kline+xx-x+r]
				}
			}
			out[y*e+x] = sum
		}
	}
	return out
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

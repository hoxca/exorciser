package detect

import (
	"errors"
	"fmt"
	"math"
)

// Profile is a ghost shape extracted from a dirty frame. Kernel is a
// normalised, non-negative matched-filter kernel of side 2*Radius+1, centred
// on its middle pixel.
type Profile struct {
	Kernel []float64
	Radius int

	// X and Y are the recentred artefact position in the dirty frame. ShiftX
	// and ShiftY are the offset from the requested centre.
	X, Y           int
	ShiftX, ShiftY int

	// Noise is the residual noise estimated from the extraction ring. Masked
	// is the number of clipped or dilated pixels in the extraction patch.
	Noise  float64
	Masked int
}

// ProfileOptions configures ExtractProfile.
type ProfileOptions struct {
	// X and Y are the approximate artefact centre in the dirty frame.
	X, Y int
	// Radius is the profile half-width in pixels.
	Radius int
	// Margin is the width of the background ring around the extraction patch.
	// It must exceed Radius.
	Margin int
	// Stride is the subsampling step used to fit the affine transform.
	Stride int
	// ClipSigma masks residual pixels beyond this many noise sigmas. The
	// extraction default is deliberately loose: the ghost is the signal, and a
	// tight clip would remove its core. The radial median rejects the remaining
	// compact outliers.
	ClipSigma float64
}

func (o ProfileOptions) withDefaults() ProfileOptions {
	if o.Radius == 0 {
		o.Radius = 16
	}
	if o.Margin == 0 {
		o.Margin = 64
	}
	if o.Stride == 0 {
		o.Stride = 8
	}
	if o.ClipSigma == 0 {
		o.ClipSigma = 20
	}
	return o
}

// ExtractProfile measures a ghost in a dirty frame against the clean reference
// and returns a radial shape hint for the matched filter.
//
// The two frames are aligned by the same affine fit as Score, then a local
// background plane is removed. The residual patch around (X, Y) is lightly
// clipped, smoothed, and reduced to a radial median so that per-pixel noise and
// a few hot pixels do not become part of the kernel. The profile is recentred
// on the brightest compact residual within half its radius, the outer bins are
// used as a baseline, negative bins are discarded, and the small residual
// wings are zeroed. The resulting kernel is positive-only and normalised to
// sum one.
func ExtractProfile(frame, ref []float32, w, h int, opts ProfileOptions) (Profile, error) {
	opts = opts.withDefaults()
	if opts.Radius < 1 {
		return Profile{}, fmt.Errorf("profile radius must be positive, got %d", opts.Radius)
	}
	if opts.Margin <= opts.Radius {
		return Profile{}, fmt.Errorf("profile margin %d must exceed the profile radius %d",
			opts.Margin, opts.Radius)
	}

	side := 2*opts.Radius + 1
	win := At(opts.X, opts.Y, side)
	prep, err := prepare(frame, ref, w, h, win, opts.Radius, opts.Margin, opts.Stride, opts.ClipSigma)
	if err != nil {
		return Profile{}, err
	}

	patch, valid, err := cropPatch(prep, opts.Radius)
	if err != nil {
		return Profile{}, err
	}
	if valid[opts.Radius*side+opts.Radius] == 0 {
		return Profile{}, errors.New("dirty artefact centre is masked or clipped")
	}
	usable, total := diskPixels(valid, opts.Radius)
	if usable < total/4 {
		return Profile{}, fmt.Errorf("dirty profile patch is mostly masked: %d of %d pixels usable",
			usable, total)
	}

	smooth := smoothPatch(patch, valid, side)
	if centralMean(smooth, valid, side, opts.Radius) <= 0 {
		return Profile{}, errors.New("dirty patch has no positive profile")
	}
	shiftX, shiftY := recentre(smooth, valid, side, opts.Radius)
	radial, se, err := radialProfile(smooth, valid, side, opts.Radius, shiftX, shiftY)
	if err != nil {
		return Profile{}, err
	}
	radial = smoothRadial(radial)
	radial = baselineRadial(radial)

	peak := 0.0
	for _, v := range radial {
		peak = max(peak, v)
	}
	if peak <= 0 {
		return Profile{}, errors.New("dirty patch has no positive profile")
	}
	// Discard the noise floor so the kernel's tails do not integrate noise.
	// The radial median's standard error comes from the dispersion within each
	// annulus; a bin must clear both it and a small fraction of the peak.
	cut := 0.02 * peak
	for r, v := range radial {
		if v < cut || v < 1.5*se[r] {
			radial[r] = 0
		}
	}

	kernel := buildProfileKernel(radial, opts.Radius)
	var sum float64
	for _, v := range kernel {
		sum += v
	}
	if sum <= 0 {
		return Profile{}, errors.New("dirty profile is empty after thresholding")
	}
	for i := range kernel {
		kernel[i] /= sum
	}

	masked := 0
	for _, v := range valid {
		if v == 0 {
			masked++
		}
	}

	return Profile{
		Kernel: kernel,
		Radius: opts.Radius,
		X:      opts.X + shiftX,
		Y:      opts.Y + shiftY,
		ShiftX: shiftX,
		ShiftY: shiftY,
		Noise:  prep.noise,
		Masked: masked,
	}, nil
}

// validate checks a profile supplied to Score. The kernel need not be
// normalised; Score normalises a copy.
func (p *Profile) validate() error {
	if p == nil {
		return nil
	}
	if p.Radius < 1 {
		return fmt.Errorf("ghost profile radius must be positive, got %d", p.Radius)
	}
	side := 2*p.Radius + 1
	if len(p.Kernel) != side*side {
		return fmt.Errorf("ghost profile has %d pixels, want %d for radius %d",
			len(p.Kernel), side*side, p.Radius)
	}
	var sum float64
	for _, v := range p.Kernel {
		if !finite(v) || v < 0 {
			return errors.New("ghost profile has a negative or non-finite weight")
		}
		sum += v
	}
	if sum <= 0 {
		return errors.New("ghost profile is empty")
	}
	return nil
}

// normalized returns a copy of the kernel scaled to sum one.
func (p *Profile) normalized() []float64 {
	var sum float64
	for _, v := range p.Kernel {
		sum += v
	}
	out := make([]float64, len(p.Kernel))
	for i, v := range p.Kernel {
		out[i] = v / sum
	}
	return out
}

// cropPatch returns the central (2r+1)² square of the prepared support.
func cropPatch(prep prepared, r int) (patch, valid []float64, err error) {
	side := 2*r + 1
	off := prep.ext/2 - r
	if off < 0 || off+side > prep.ext {
		return nil, nil, errors.New("profile patch does not fit the prepared support")
	}
	patch = make([]float64, side*side)
	valid = make([]float64, side*side)
	for j := 0; j < side; j++ {
		src := (off + j) * prep.ext
		copy(patch[j*side:(j+1)*side], prep.res[src+off:src+off+side])
		copy(valid[j*side:(j+1)*side], prep.valid[src+off:src+off+side])
	}
	return patch, valid, nil
}

// smoothPatch applies a valid-aware 3×3 mean to the residual patch. The mean
// cuts per-pixel noise without spreading the ghost much beyond its core.
func smoothPatch(patch, valid []float64, side int) []float64 {
	out := make([]float64, len(patch))
	for j := 0; j < side; j++ {
		for i := 0; i < side; i++ {
			idx := j*side + i
			if valid[idx] == 0 {
				continue
			}
			var sum float64
			var n int
			for dy := -1; dy <= 1; dy++ {
				yy := j + dy
				if yy < 0 || yy >= side {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					xx := i + dx
					if xx < 0 || xx >= side {
						continue
					}
					k := yy*side + xx
					if valid[k] == 0 {
						continue
					}
					sum += patch[k]
					n++
				}
			}
			if n > 0 {
				out[idx] = sum / float64(n)
			}
		}
	}
	return out
}

// diskPixels counts usable and total pixels inside the profile radius.
func diskPixels(valid []float64, radius int) (usable, total int) {
	side := 2*radius + 1
	for j := 0; j < side; j++ {
		for i := 0; i < side; i++ {
			dx, dy := i-radius, j-radius
			if dx*dx+dy*dy > radius*radius {
				continue
			}
			total++
			if valid[j*side+i] != 0 {
				usable++
			}
		}
	}
	return usable, total
}

// centralMean returns the mean smoothed residual of the valid pixels in a
// small disk around the requested centre. It separates a real bright patch
// from a dark one before recentring can wander onto a positive noise peak.
func centralMean(smooth, valid []float64, side, radius int) float64 {
	r := max(2, radius/3)
	var sum float64
	var n int
	for j := radius - r; j <= radius+r; j++ {
		for i := radius - r; i <= radius+r; i++ {
			dx, dy := i-radius, j-radius
			if dx*dx+dy*dy > r*r {
				continue
			}
			idx := j*side + i
			if valid[idx] == 0 {
				continue
			}
			sum += smooth[idx]
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// recentre returns the intensity-weighted centroid of the positive smoothed
// residual within half the profile radius, offset from the patch middle. A
// centroid is steadier than a single maximum when the feature is faint.
func recentre(smooth, valid []float64, side, radius int) (int, int) {
	limit := max(1, radius/2)
	cx, cy := radius, radius
	var sx, sy, sw float64
	for j := cy - limit; j <= cy+limit; j++ {
		for i := cx - limit; i <= cx+limit; i++ {
			if i < 0 || i >= side || j < 0 || j >= side {
				continue
			}
			idx := j*side + i
			if valid[idx] == 0 {
				continue
			}
			v := smooth[idx]
			if v <= 0 {
				continue
			}
			sx += float64(i) * v
			sy += float64(j) * v
			sw += v
		}
	}
	if sw <= 0 {
		return 0, 0
	}
	return int(math.Round(sx/sw)) - cx, int(math.Round(sy/sw)) - cy
}

// radialProfile reduces the smoothed patch to the median value in each integer
// radius bin around the recentred position, and returns the standard error of
// each median. Empty interior bins are interpolated; empty edge bins repeat the
// nearest value.
func radialProfile(smooth, valid []float64, side, radius, shiftX, shiftY int) (radial, se []float64, err error) {
	bins := make([][]float64, radius+1)
	cx, cy := radius+shiftX, radius+shiftY
	for j := 0; j < side; j++ {
		for i := 0; i < side; i++ {
			idx := j*side + i
			if valid[idx] == 0 {
				continue
			}
			dx := float64(i - cx)
			dy := float64(j - cy)
			r := int(math.Round(math.Hypot(dx, dy)))
			if r > radius {
				continue
			}
			bins[r] = append(bins[r], smooth[idx])
		}
	}

	radial = make([]float64, radius+1)
	se = make([]float64, radius+1)
	for r := range radial {
		radial[r] = math.NaN()
		se[r] = math.NaN()
		if len(bins[r]) == 0 {
			continue
		}
		median, mad := robustScale(bins[r])
		radial[r] = median
		// Standard error of a median for a normal sample: 1.253*sigma/sqrt(n),
		// with the MAD standing in for sigma.
		se[r] = 1.253 * mad / math.Sqrt(float64(len(bins[r])))
	}

	first, last := -1, -1
	for r, v := range radial {
		if math.IsNaN(v) {
			continue
		}
		if first < 0 {
			first = r
		}
		last = r
	}
	if first < 0 {
		return nil, nil, errors.New("no usable pixels for the radial profile")
	}
	for r := 0; r < first; r++ {
		radial[r] = radial[first]
		se[r] = se[first]
	}
	for r := last + 1; r <= radius; r++ {
		radial[r] = radial[last]
		se[r] = se[last]
	}
	prev := first
	for r := first + 1; r <= last; r++ {
		if math.IsNaN(radial[r]) {
			continue
		}
		if r-prev > 1 {
			for k := prev + 1; k < r; k++ {
				t := float64(k-prev) / float64(r-prev)
				radial[k] = radial[prev]*(1-t) + radial[r]*t
				se[k] = se[prev]*(1-t) + se[r]*t
			}
		}
		prev = r
	}
	return radial, se, nil
}

// smoothRadial applies a [1,2,1]/4 moving average to the radial profile.
func smoothRadial(radial []float64) []float64 {
	out := make([]float64, len(radial))
	for r := range radial {
		var sum, weight float64
		for k := -1; k <= 1; k++ {
			rr := r + k
			if rr < 0 || rr >= len(radial) {
				continue
			}
			w := 0.25
			if k == 0 {
				w = 0.5
			}
			sum += w * radial[rr]
			weight += w
		}
		out[r] = sum / weight
	}
	return out
}

// baselineRadial subtracts the outer-quarter median and clips negatives. A
// negative baseline is ignored: the plane fit already removed the background,
// and adding a pedestal would inflate the kernel's wings.
func baselineRadial(radial []float64) []float64 {
	n := len(radial)
	start := 3 * (n - 1) / 4
	if start < 1 {
		start = 1
	}
	if start >= n {
		start = n - 1
	}
	base, _ := robustScale(radial[start:])
	if base < 0 {
		base = 0
	}
	out := make([]float64, n)
	for r, v := range radial {
		v -= base
		if v < 0 {
			v = 0
		}
		out[r] = v
	}
	return out
}

// buildProfileKernel expands a radial profile into a (2r+1)² kernel.
func buildProfileKernel(radial []float64, radius int) []float64 {
	side := 2*radius + 1
	kernel := make([]float64, side*side)
	for j := 0; j < side; j++ {
		for i := 0; i < side; i++ {
			dx := float64(i - radius)
			dy := float64(j - radius)
			r := int(math.Round(math.Hypot(dx, dy)))
			if r <= radius {
				kernel[j*side+i] = radial[r]
			}
		}
	}
	return kernel
}

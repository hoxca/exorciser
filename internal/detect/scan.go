package detect

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"sync"
)

// ScanOptions configures a blind search of a whole image.
type ScanOptions struct {
	// Scales are the Gaussian sigmas, in pixels, tried by the matched filter.
	Scales []float64
	// MinSNR is the significance at which a peak is reported.
	MinSNR float64
	// Stride is the subsampling step used to estimate the background.
	Stride int
	// Workers is the number of rows processed concurrently.
	Workers int
	// Mask marks pixels to exclude, for example the stars of the clean master.
	// A nil mask excludes nothing. The slice must cover the image.
	Mask []bool
}

func (o ScanOptions) withDefaults() ScanOptions {
	if len(o.Scales) == 0 {
		o.Scales = []float64{4, 8, 16}
	}
	if o.MinSNR == 0 {
		o.MinSNR = 6
	}
	if o.Stride == 0 {
		o.Stride = 8
	}
	if o.Workers == 0 {
		o.Workers = runtime.GOMAXPROCS(0)
	}
	return o
}

func (o ScanOptions) validate() error {
	if o.MinSNR <= 0 || !finite(o.MinSNR) {
		return fmt.Errorf("minimum SNR must be a positive number, got %v", o.MinSNR)
	}
	if o.Stride < 1 {
		return fmt.Errorf("background stride must be positive, got %d", o.Stride)
	}
	if o.Workers < 1 {
		return fmt.Errorf("workers must be positive, got %d", o.Workers)
	}
	for _, s := range o.Scales {
		if s <= 0 || !finite(s) {
			return fmt.Errorf("ghost scale must be positive, got %v", s)
		}
	}
	return nil
}

// Artefact is one diffuse feature found by Scan. SNR and Amp are signed, so a
// darker-than-background feature is reported with negative values.
type Artefact struct {
	X     int
	Y     int
	Scale float64
	SNR   float64
	Amp   float64
}

// Scan finds every significant diffuse feature in plane.
//
// It removes a robust background plane, masks non-finite pixels and dilates the
// mask so dead columns cannot feed the filter, then matched-filters the residual
// at every scale. A peak is reported when its signal-to-noise ratio reaches
// MinSNR. Peaks closer than the kernel radius are merged, strongest first.
//
// Asteroids, stars and other objects common to both stacks cancel before the
// search. A ghost, which differs between them, does not. No amplitude threshold
// is applied to the difference, because averaging raises the ghost above the
// noise: such a mask would remove the very feature being searched for. An
// external spatial mask, typically stars found in the clean master, may be
// supplied in Mask.
func Scan(plane []float32, w, h int, opts ScanOptions) ([]Artefact, error) {
	opts = opts.withDefaults()
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if w <= 0 || h <= 0 {
		return nil, errors.New("image has no pixels")
	}
	if len(plane) < w*h {
		return nil, errors.New("pixel buffer is smaller than the image")
	}
	if opts.Mask != nil && len(opts.Mask) < w*h {
		return nil, errors.New("mask is smaller than the image")
	}

	bg, noise, err := scanBackground(plane, w, h, opts.Stride)
	if err != nil {
		return nil, err
	}
	if noise <= 0 {
		return nil, errors.New("background noise estimate is zero")
	}

	res := make([]float64, w*h)
	valid := make([]float64, w*h)
	validCount := 0
	for y := 0; y < h; y++ {
		line := y * w
		for x := 0; x < w; x++ {
			i := line + x
			v := float64(plane[i])
			if !finite(v) {
				continue
			}
			res[i] = v - bg.at(x, y, w/2, h/2)
			valid[i] = 1
			validCount++
		}
	}
	if validCount == 0 {
		return nil, errors.New("every pixel was masked")
	}
	for i := 0; i < w*h; i++ {
		if opts.Mask != nil && opts.Mask[i] && valid[i] != 0 {
			valid[i] = 0
			res[i] = 0
			validCount--
		}
	}
	if validCount == 0 {
		return nil, errors.New("every pixel was masked")
	}
	dilateInvalidPlane(valid, w, h, maskDilation)
	for i := range res {
		if valid[i] == 0 {
			res[i] = 0
		}
	}

	var candidates []Artefact
	for _, scale := range opts.Scales {
		found, err := scanScale(res, valid, w, h, noise, scale, opts)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, found...)
	}
	return mergeArtefacts(candidates), nil
}

// StarMaskOptions configures StarMask.
type StarMaskOptions struct {
	// Sigma is the detection threshold in sigmas of the local-mean residual.
	Sigma float64
	// BoxRadius is the half width of the local-mean box, in pixels.
	BoxRadius int
	// Dilation grows the mask by this many pixels.
	Dilation int
	// Border masks a frame of this width around the image.
	Border int
	// Workers is the number of rows processed concurrently.
	Workers int
}

func (o StarMaskOptions) withDefaults() StarMaskOptions {
	if o.Sigma == 0 {
		o.Sigma = 50
	}
	if o.BoxRadius == 0 {
		o.BoxRadius = 4
	}
	if o.Dilation == 0 {
		o.Dilation = 4
	}
	if o.Workers == 0 {
		o.Workers = runtime.GOMAXPROCS(0)
	}
	return o
}

func (o StarMaskOptions) validate() error {
	if o.Sigma <= 0 || !finite(o.Sigma) {
		return fmt.Errorf("star threshold must be a positive number, got %v", o.Sigma)
	}
	if o.BoxRadius < 1 {
		return fmt.Errorf("star box radius must be positive, got %d", o.BoxRadius)
	}
	if o.Dilation < 0 {
		return fmt.Errorf("star mask dilation must not be negative, got %d", o.Dilation)
	}
	if o.Border < 0 {
		return fmt.Errorf("border must not be negative, got %d", o.Border)
	}
	if o.Workers < 1 {
		return fmt.Errorf("workers must be positive, got %d", o.Workers)
	}
	return nil
}

// StarMask marks compact, bright features in a clean master.
//
// A star is much smaller than the local-mean box, so its centre stands far
// above the mean of its neighbourhood. A nebula or a ghost fills the box, so
// its residual stays near the noise. The threshold is measured from the
// residual with a MAD, which stars do not bias. The mask is dilated so the
// filter never sees a star's core. It is built from the clean master, so a
// ghost present only in the detected group is not masked.
func StarMask(plane []float32, w, h int, opts StarMaskOptions) ([]bool, error) {
	opts = opts.withDefaults()
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if w <= 0 || h <= 0 {
		return nil, errors.New("image has no pixels")
	}
	if len(plane) < w*h {
		return nil, errors.New("pixel buffer is smaller than the image")
	}

	mask := make([]bool, w*h)
	res := make([]float64, w*h)
	valid := make([]float64, w*h)
	for i := 0; i < w*h; i++ {
		v := float64(plane[i])
		if !finite(v) {
			mask[i] = true
			continue
		}
		res[i] = v
		valid[i] = 1
	}

	box := make([]float64, 2*opts.BoxRadius+1)
	for i := range box {
		box[i] = 1
	}
	num := convolvePlane(res, w, h, box, opts.Workers)
	den := convolvePlane(valid, w, h, box, opts.Workers)
	detail := make([]float64, w*h)
	for i := range detail {
		if den[i] > 0 && valid[i] != 0 {
			detail[i] = res[i] - num[i]/den[i]
		}
	}
	if _, mad := robustScale(detail); mad > 0 {
		limit := opts.Sigma * 1.4826 * mad
		for i, v := range detail {
			if valid[i] != 0 && math.Abs(v) > limit {
				mask[i] = true
			}
		}
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < opts.Border || x >= w-opts.Border || y < opts.Border || y >= h-opts.Border {
				mask[y*w+x] = true
			}
		}
	}
	dilateMask(mask, w, h, opts.Dilation)
	return mask, nil
}

func dilateMask(mask []bool, w, h, r int) {
	if r <= 0 {
		return
	}
	bad := make([]bool, len(mask))
	copy(bad, mask)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !bad[y*w+x] {
				continue
			}
			for dy := -r; dy <= r; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				line := yy * w
				for dx := -r; dx <= r; dx++ {
					xx := x + dx
					if xx < 0 || xx >= w {
						continue
					}
					mask[line+xx] = true
				}
			}
		}
	}
}

// backgroundBlock is the number of subsample points per side whose median
// represents one background regression point.
const backgroundBlock = 4

// scanBackground fits a plane to block medians and estimates the per-pixel
// noise from the residual of a stride-sampled subset.
func scanBackground(img []float32, w, h, stride int) (plane, float64, error) {
	us, vs, zs := backgroundSamples(img, w, h, stride)
	if len(zs) == 0 {
		return plane{}, 0, errors.New("no finite pixels to estimate the background")
	}

	var bg plane
	if len(zs) < 50 {
		// A plane needs enough regression points; on a small frame the median
		// is the safer background.
		median, _ := robustScale(zs)
		bg = plane{c0: median}
	} else {
		var err error
		bg, _, err = fitBackground(us, vs, zs)
		if err != nil {
			return plane{}, 0, fmt.Errorf("fit background: %w", err)
		}
	}

	samples := make([]float64, 0, (w/stride+1)*(h/stride+1))
	cx, cy := w/2, h/2
	for y := 0; y < h; y += stride {
		for x := 0; x < w; x += stride {
			v := float64(img[y*w+x])
			if !finite(v) {
				continue
			}
			samples = append(samples, v-bg.at(x, y, cx, cy))
		}
	}
	if len(samples) < 2 {
		return plane{}, 0, errors.New("too few pixels to estimate the noise")
	}
	_, mad := robustScale(samples)
	if mad == 0 {
		return plane{}, 0, errors.New("background noise estimate is zero")
	}
	return bg, 1.4826 * mad, nil
}

// backgroundSamples reduces plane to medians over backgroundBlock×backgroundBlock
// subsample points on a stride grid.
func backgroundSamples(img []float32, w, h, stride int) (us, vs, zs []float64) {
	gridW := (w + stride - 1) / stride
	gridH := (h + stride - 1) / stride
	buf := make([]float64, 0, backgroundBlock*backgroundBlock)

	for by := 0; by < gridH; by += backgroundBlock {
		for bx := 0; bx < gridW; bx += backgroundBlock {
			buf = buf[:0]
			var sumX, sumY float64
			var n int
			for j := by; j < by+backgroundBlock && j < gridH; j++ {
				y := j * stride
				line := y * w
				for i := bx; i < bx+backgroundBlock && i < gridW; i++ {
					x := i * stride
					v := float64(img[line+x])
					if !finite(v) {
						continue
					}
					buf = append(buf, v)
					sumX += float64(x)
					sumY += float64(y)
					n++
				}
			}
			if n < backgroundBlock {
				continue
			}
			median, _ := robustScale(buf)
			us = append(us, sumX/float64(n)-float64(w/2))
			vs = append(vs, sumY/float64(n)-float64(h/2))
			zs = append(zs, median)
		}
	}
	return us, vs, zs
}

// scanScale matched-filters one scale and returns its non-maximum-suppressed
// candidates.
func scanScale(res, valid []float64, w, h int, noise, scale float64, opts ScanOptions) ([]Artefact, error) {
	radius := int(math.Ceil(3 * scale))
	kernel := gaussianWeights(radius, scale)

	signal := convolvePlane(res, w, h, kernel, opts.Workers)
	weight := convolvePlane(valid, w, h, kernel, opts.Workers)
	noiseSq := convolvePlane(valid, w, h, squared(kernel), opts.Workers)

	var sum float64
	for _, k := range kernel {
		sum += k
	}
	fullWeight := sum * sum

	var candidates []Artefact
	for y := 0; y < h; y++ {
		line := y * w
		for x := 0; x < w; x++ {
			i := line + x
			if weight[i] <= 0.5*fullWeight || noiseSq[i] <= 0 {
				continue
			}
			snr := signal[i] / (noise * math.Sqrt(noiseSq[i]))
			if math.Abs(snr) < opts.MinSNR {
				continue
			}
			candidates = append(candidates, Artefact{
				X:     x,
				Y:     y,
				Scale: scale,
				SNR:   snr,
				Amp:   signal[i] / weight[i],
			})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	slices.SortFunc(candidates, compareArtefacts)
	mergeRadius := 4 * scale
	kept := candidates[:0]
	for _, c := range candidates {
		duplicate := false
		for _, k := range kept {
			if within(c, k, mergeRadius) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			kept = append(kept, c)
		}
	}
	return kept, nil
}

// mergeArtefacts combines the per-scale candidates, keeping the strongest of
// each group that shares a position.
func mergeArtefacts(candidates []Artefact) []Artefact {
	if len(candidates) == 0 {
		return nil
	}
	slices.SortFunc(candidates, compareArtefacts)
	kept := candidates[:0]
	for _, c := range candidates {
		duplicate := false
		for _, k := range kept {
			radius := max(3.0, 3*max(c.Scale, k.Scale))
			if within(c, k, radius) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			kept = append(kept, c)
		}
	}
	return kept
}

func within(a, b Artefact, radius float64) bool {
	dx := float64(a.X - b.X)
	dy := float64(a.Y - b.Y)
	return dx*dx+dy*dy <= radius*radius
}

// compareArtefacts orders by decreasing |SNR|, then by position for determinism.
func compareArtefacts(a, b Artefact) int {
	aa, bb := math.Abs(a.SNR), math.Abs(b.SNR)
	switch {
	case aa > bb:
		return -1
	case aa < bb:
		return 1
	case a.Y < b.Y:
		return -1
	case a.Y > b.Y:
		return 1
	case a.X < b.X:
		return -1
	case a.X > b.X:
		return 1
	default:
		return 0
	}
}

// convolvePlane convolves a w×h plane with a separable kernel. Pixels outside
// the plane are treated as zero; callers that need edge-aware normalisation
// convolve their valid mask with the same kernel. Rows are processed by
// workers goroutines.
func convolvePlane(src []float64, w, h int, kernel []float64, workers int) []float64 {
	r := len(kernel) / 2
	tmp := make([]float64, w*h)
	out := make([]float64, w*h)

	parallelRows(h, workers, func(y int) {
		line := y * w
		for x := 0; x < w; x++ {
			var sum float64
			lo, hi := max(0, x-r), min(w-1, x+r)
			for xx := lo; xx <= hi; xx++ {
				sum += src[line+xx] * kernel[xx-x+r]
			}
			tmp[line+x] = sum
		}
	})

	parallelRows(h, workers, func(y int) {
		line := y * w
		lo, hi := max(0, y-r), min(h-1, y+r)
		for x := 0; x < w; x++ {
			var sum float64
			for yy := lo; yy <= hi; yy++ {
				sum += tmp[yy*w+x] * kernel[yy-y+r]
			}
			out[line+x] = sum
		}
	})
	return out
}

// dilateInvalidPlane clears valid pixels within r pixels of an invalid one.
func dilateInvalidPlane(valid []float64, w, h, r int) {
	bad := make([]bool, len(valid))
	any := false
	for i, v := range valid {
		if v == 0 {
			bad[i] = true
			any = true
		}
	}
	if !any {
		return
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !bad[y*w+x] {
				continue
			}
			for dy := -r; dy <= r; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				line := yy * w
				for dx := -r; dx <= r; dx++ {
					xx := x + dx
					if xx < 0 || xx >= w {
						continue
					}
					valid[line+xx] = 0
				}
			}
		}
	}
}

func parallelRows(h, workers int, fn func(y int)) {
	workers = max(1, min(workers, h))
	if workers == 1 {
		for y := 0; y < h; y++ {
			fn(y)
		}
		return
	}

	rows := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for y := range rows {
				fn(y)
			}
		}()
	}
	for y := 0; y < h; y++ {
		rows <- y
	}
	close(rows)
	wg.Wait()
}

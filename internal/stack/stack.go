// Package stack combines aligned frames into a master frame. Normalise maps
// each frame onto a reference with an affine transform, Average rejects
// outliers with per-pixel sigma clipping, and Subtract builds a difference
// image.
package stack

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"sync"

	"github.com/hoxca/exorciser/internal/detect"
)

// Options configures Average.
type Options struct {
	// Sigma is the clipping threshold in robust sigmas.
	Sigma float64
	// Iterations is the number of clipping passes.
	Iterations int
	// MinValid is the smallest number of surviving samples a pixel may have.
	MinValid int
	// Workers is the number of rows processed concurrently.
	Workers int
}

func (o Options) withDefaults() Options {
	if o.Sigma == 0 {
		o.Sigma = 3
	}
	if o.Iterations == 0 {
		o.Iterations = 3
	}
	if o.MinValid == 0 {
		o.MinValid = 2
	}
	if o.Workers == 0 {
		o.Workers = runtime.GOMAXPROCS(0)
	}
	return o
}

func (o Options) validate() error {
	if o.Sigma <= 0 || math.IsNaN(o.Sigma) || math.IsInf(o.Sigma, 0) {
		return fmt.Errorf("clip sigma must be a positive number, got %v", o.Sigma)
	}
	if o.Iterations < 1 {
		return fmt.Errorf("clip iterations must be positive, got %d", o.Iterations)
	}
	if o.MinValid < 1 {
		return fmt.Errorf("minimum valid samples must be positive, got %d", o.MinValid)
	}
	if o.Workers < 1 {
		return fmt.Errorf("workers must be positive, got %d", o.Workers)
	}
	return nil
}

// Result is a stacked plane.
type Result struct {
	// Plane holds the clipped mean. Pixels with fewer than MinValid surviving
	// samples are NaN.
	Plane []float32
	// Rejected counts the samples removed by clipping.
	Rejected int64
	// Invalid counts the pixels left without enough samples.
	Invalid int
}

// Normalize rewrites frame so that it matches ref: after the call,
// frame ≈ ref. It estimates frame ≈ a*ref + b on block medians and applies the
// inverse transform in place, so the caller can drop its raw copy.
func Normalize(frame, ref []float32, w, h, stride int) (detect.Affine, error) {
	aff, err := detect.Fit(frame, ref, w, h, stride)
	if err != nil {
		return aff, err
	}
	if aff.A == 0 || math.IsNaN(aff.A) || math.IsInf(aff.A, 0) {
		return aff, fmt.Errorf("normalization slope is %v", aff.A)
	}
	if len(frame) < w*h || len(ref) < w*h {
		return aff, errors.New("pixel buffers are smaller than the image")
	}

	inv := 1 / aff.A
	for i := 0; i < w*h; i++ {
		frame[i] = float32((float64(frame[i]) - aff.B) * inv)
	}
	return aff, nil
}

// Average sigma-clips frames per pixel and returns their mean.
//
// Clipping uses the median and median absolute deviation of the valid samples,
// which is robust with only ten frames. Values beyond Sigma robust sigmas are
// dropped and the statistic is recomputed, up to Iterations times. Non-finite
// samples are ignored.
func Average(frames [][]float32, w, h int, opts Options) (Result, error) {
	opts = opts.withDefaults()
	if err := opts.validate(); err != nil {
		return Result{}, err
	}
	if w <= 0 || h <= 0 {
		return Result{}, errors.New("image has no pixels")
	}
	if len(frames) == 0 {
		return Result{}, errors.New("no frames to stack")
	}
	if opts.MinValid > len(frames) {
		return Result{}, fmt.Errorf("minimum valid samples %d exceeds the %d frames",
			opts.MinValid, len(frames))
	}
	for i, f := range frames {
		if len(f) < w*h {
			return Result{}, fmt.Errorf("frame %d has %d pixels, want %d", i, len(f), w*h)
		}
	}

	out := make([]float32, w*h)
	workers := min(opts.Workers, h)
	rejected := make([]int64, workers)
	invalid := make([]int, workers)

	rows := make(chan int)
	var wg sync.WaitGroup
	for k := 0; k < workers; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			vals := make([]float64, 0, len(frames))
			scratch := make([]float64, len(frames))
			for y := range rows {
				line := y * w
				for x := 0; x < w; x++ {
					i := line + x
					vals = vals[:0]
					for _, f := range frames {
						v := float64(f[i])
						if math.IsNaN(v) || math.IsInf(v, 0) {
							continue
						}
						vals = append(vals, v)
					}
					if len(vals) < opts.MinValid {
						out[i] = nan()
						invalid[k]++
						continue
					}

					kept, dropped := clip(vals, scratch, opts)
					rejected[k] += int64(dropped)
					if len(kept) < opts.MinValid {
						out[i] = nan()
						invalid[k]++
						continue
					}

					var sum float64
					for _, v := range kept {
						sum += v
					}
					out[i] = float32(sum / float64(len(kept)))
				}
			}
		}(k)
	}
	for y := 0; y < h; y++ {
		rows <- y
	}
	close(rows)
	wg.Wait()

	result := Result{Plane: out}
	for k := range rejected {
		result.Rejected += rejected[k]
		result.Invalid += invalid[k]
	}
	return result, nil
}

// clip returns the surviving prefix of vals and how many samples it removed.
// scratch must have room for len(vals).
func clip(vals, scratch []float64, opts Options) ([]float64, int) {
	kept := vals
	dropped := 0

	for iter := 0; iter < opts.Iterations; iter++ {
		if len(kept) <= opts.MinValid {
			break
		}
		scratch = scratch[:len(kept)]
		copy(scratch, kept)
		slices.Sort(scratch)
		median := medianSorted(scratch)

		for i, v := range kept {
			scratch[i] = math.Abs(v - median)
		}
		slices.Sort(scratch)
		mad := medianSorted(scratch)
		if mad == 0 {
			break
		}

		limit := opts.Sigma * 1.4826 * mad
		out := kept[:0]
		for _, v := range kept {
			if math.Abs(v-median) <= limit {
				out = append(out, v)
			}
		}
		if len(out) == len(kept) {
			break
		}
		dropped += len(kept) - len(out)
		kept = out
	}
	return kept, dropped
}

func medianSorted(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// Subtract returns a-b, propagating non-finite samples.
func Subtract(a, b []float32) ([]float32, error) {
	if len(a) != len(b) {
		return nil, fmt.Errorf("cannot subtract planes of %d and %d pixels", len(a), len(b))
	}
	out := make([]float32, len(a))
	for i := range a {
		out[i] = a[i] - b[i]
	}
	return out, nil
}

func nan() float32 {
	return float32(math.NaN())
}

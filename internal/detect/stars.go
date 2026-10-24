package detect

import (
	"errors"
	"fmt"
	"slices"
)

// Star is a connected region of saturated pixels in a clean master.
type Star struct {
	// X and Y are the centroid in image pixels.
	X, Y float64
	// Area is the number of saturated pixels in the component.
	Area int
}

// StarComponents labels the pixels brighter than min and returns one Star for
// each 8-connected component. The clean master's brightest stars saturate over
// a large area, while fainter stars saturate over only a few pixels, so Area is
// a useful proxy for the size of the star's halo.
func StarComponents(plane []float32, w, h int, min float64) ([]Star, error) {
	if w <= 0 || h <= 0 {
		return nil, errors.New("image has no pixels")
	}
	if len(plane) < w*h {
		return nil, errors.New("pixel buffer is smaller than the image")
	}
	if min <= 0 || !finite(min) {
		return nil, fmt.Errorf("star saturation threshold must be a positive number, got %v", min)
	}

	seen := make([]bool, w*h)
	queue := make([]int, 0, 64)
	stars := make([]Star, 0, 16)
	for i := 0; i < w*h; i++ {
		if seen[i] {
			continue
		}
		v := float64(plane[i])
		if !finite(v) || v <= min {
			continue
		}

		seen[i] = true
		queue = append(queue[:0], i)
		var sumX, sumY int64
		count := 0
		for head := 0; head < len(queue); head++ {
			j := queue[head]
			x, y := j%w, j/w
			sumX += int64(x)
			sumY += int64(y)
			count++
			for dy := -1; dy <= 1; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					xx := x + dx
					if xx < 0 || xx >= w {
						continue
					}
					next := yy*w + xx
					if seen[next] {
						continue
					}
					nv := float64(plane[next])
					if !finite(nv) || nv <= min {
						continue
					}
					seen[next] = true
					queue = append(queue, next)
				}
			}
		}
		stars = append(stars, Star{
			X:    float64(sumX) / float64(count),
			Y:    float64(sumY) / float64(count),
			Area: count,
		})
	}
	slices.SortFunc(stars, func(a, b Star) int {
		if a.Area != b.Area {
			return b.Area - a.Area
		}
		if a.Y != b.Y {
			if a.Y < b.Y {
				return -1
			}
			return 1
		}
		if a.X < b.X {
			return -1
		}
		if a.X > b.X {
			return 1
		}
		return 0
	})
	return stars, nil
}

// RejectStarHaloes removes artefacts that lie in a star's halo. An artefact is
// rejected when it is within small of any star, or within big of a star whose
// saturated area is at least minArea. A non-positive radius disables that rule.
// The order of the surviving artefacts is preserved.
func RejectStarHaloes(arts []Artefact, stars []Star, small, big float64, minArea int) ([]Artefact, int) {
	if len(arts) == 0 || len(stars) == 0 || (small <= 0 && big <= 0) {
		return arts, 0
	}
	small2 := small * small
	big2 := big * big
	kept := make([]Artefact, 0, len(arts))
	rejected := 0
	for _, a := range arts {
		drop := false
		for _, s := range stars {
			dx := float64(a.X) - s.X
			dy := float64(a.Y) - s.Y
			d2 := dx*dx + dy*dy
			if small > 0 && d2 <= small2 {
				drop = true
				break
			}
			if big > 0 && s.Area >= minArea && d2 <= big2 {
				drop = true
				break
			}
		}
		if drop {
			rejected++
			continue
		}
		kept = append(kept, a)
	}
	return kept, rejected
}

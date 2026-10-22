package detect

import "fmt"

// Window is a square region of an image.
type Window struct {
	// X and Y are the window's top-left corner, in pixels.
	X, Y int
	// Size is the window's side length, in pixels.
	Size int
}

// Centre returns the size×size window centred on a w×h image. When the
// leftover margin is odd the window sits one pixel closer to the origin.
func Centre(w, h, size int) (Window, error) {
	if size <= 0 {
		return Window{}, fmt.Errorf("window size must be positive, got %d", size)
	}
	if w < size || h < size {
		return Window{}, fmt.Errorf("window of %d×%d pixels does not fit a %d×%d image", size, size, w, h)
	}
	return Window{X: (w - size) / 2, Y: (h - size) / 2, Size: size}, nil
}

// At returns the size×size window centred on (x, y). The coordinate is the
// window's middle pixel: exact for an odd size, half a pixel further right and
// down for an even one. It does not check that the window lies inside an
// image; use Validate for that.
func At(x, y, size int) Window {
	return Window{X: x - size/2, Y: y - size/2, Size: size}
}

// Validate reports whether win lies entirely inside an imgW×imgH image.
func (win Window) Validate(imgW, imgH int) error {
	if win.Size <= 0 {
		return fmt.Errorf("window size must be positive, got %d", win.Size)
	}
	if win.X < 0 || win.Y < 0 || win.X+win.Size > imgW || win.Y+win.Size > imgH {
		return fmt.Errorf("window of %d×%d pixels at (%d,%d) does not fit a %d×%d image",
			win.Size, win.Size, win.X, win.Y, imgW, imgH)
	}
	return nil
}

package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/hoxca/exorciser/internal/detect"
)

const (
	// artefactProfilePNG is the visual export of the matched-filter kernel.
	artefactProfilePNG = "artefact_profile.png"
	// artefactProfileNPE is the lossless numerical export of the kernel.
	artefactProfileNPE = "artefact.npe"
	// profilePNGScale is how many image pixels represent one kernel sample.
	profilePNGScale = 8
)

// writeProfilePNG renders the profile kernel as a grayscale PNG. Each kernel
// sample becomes an 8×8 block, so the default 33×33 profile is readable at
// 264×264. The kernel is normalised to its peak: the brightest sample is white
// and zero weights are black.
func writeProfilePNG(path string, p detect.Profile) error {
	side, peak, err := checkProfileKernel(p)
	if err != nil {
		return err
	}

	img := image.NewGray(image.Rect(0, 0, side*profilePNGScale, side*profilePNGScale))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			v := p.Kernel[y*side+x] / peak
			gray := uint8(255 * v)
			for dy := 0; dy < profilePNGScale; dy++ {
				row := (y*profilePNGScale + dy) * side * profilePNGScale
				for dx := 0; dx < profilePNGScale; dx++ {
					img.Pix[row+x*profilePNGScale+dx] = gray
				}
			}
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// writeProfileNPE writes the exact kernel and its extraction metadata as text.
// The format is documented in the README: comment lines, then key: value
// metadata, then side rows of side space-separated full-precision samples.
func writeProfileNPE(path string, p detect.Profile, dirty string) error {
	side, _, err := checkProfileKernel(p)
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	writef := func(format string, args ...any) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	}

	if err := writef("# exorciser artefact profile\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := writef("# version: 1\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := writef("# dirty: %s\n", dirty); err != nil {
		_ = f.Close()
		return err
	}
	for _, kv := range []struct {
		key   string
		value string
	}{
		{"x", strconv.Itoa(p.X)},
		{"y", strconv.Itoa(p.Y)},
		{"shift_x", strconv.Itoa(p.ShiftX)},
		{"shift_y", strconv.Itoa(p.ShiftY)},
		{"radius", strconv.Itoa(p.Radius)},
		{"noise", strconv.FormatFloat(p.Noise, 'g', -1, 64)},
		{"masked", strconv.Itoa(p.Masked)},
		{"kernel", fmt.Sprintf("%dx%d row-major", side, side)},
	} {
		if err := writef("# %s: %s\n", kv.key, kv.value); err != nil {
			_ = f.Close()
			return err
		}
	}

	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			if x > 0 {
				if err := writef(" "); err != nil {
					_ = f.Close()
					return err
				}
			}
			v := strconv.FormatFloat(p.Kernel[y*side+x], 'g', -1, 64)
			if err := writef("%s", v); err != nil {
				_ = f.Close()
				return err
			}
		}
		if err := writef("\n"); err != nil {
			_ = f.Close()
			return err
		}
	}

	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// checkProfileKernel validates a profile and returns its side length and
// largest sample.
func checkProfileKernel(p detect.Profile) (side int, peak float64, err error) {
	if p.Radius < 1 {
		return 0, 0, fmt.Errorf("profile radius must be positive, got %d", p.Radius)
	}
	side = 2*p.Radius + 1
	if len(p.Kernel) != side*side {
		return 0, 0, fmt.Errorf("profile has %d samples, want %d for radius %d",
			len(p.Kernel), side*side, p.Radius)
	}
	for _, v := range p.Kernel {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, 0, errors.New("profile has a negative or non-finite sample")
		}
		peak = max(peak, v)
	}
	if peak <= 0 {
		return 0, 0, errors.New("profile kernel is empty")
	}
	return side, peak, nil
}

// writeArtefactProfile writes both exports and returns their paths.
func writeArtefactProfile(dir string, p detect.Profile, dirty string) (string, string, error) {
	pngPath := filepath.Join(dir, artefactProfilePNG)
	npePath := filepath.Join(dir, artefactProfileNPE)
	if err := writeProfilePNG(pngPath, p); err != nil {
		return "", "", fmt.Errorf("write %s: %w", pngPath, err)
	}
	if err := writeProfileNPE(npePath, p, dirty); err != nil {
		return "", "", fmt.Errorf("write %s: %w", npePath, err)
	}
	return pngPath, npePath, nil
}

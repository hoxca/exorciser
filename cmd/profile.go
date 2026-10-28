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
	"strings"

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

// readArtefactProfile parses an artefact.npe file. The format is produced by
// writeProfileNPE. The kernel is normalised to sum one on read.
func readArtefactProfile(path string) (detect.Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return detect.Profile{}, err
	}
	defer func() { _ = f.Close() }()

	var p detect.Profile
	var rows [][]float64
	version := ""
	side := 0
	lineNo := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			key, value, ok := strings.Cut(body, ":")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			switch key {
			case "version":
				version = value
			case "radius":
				if p.Radius, err = strconv.Atoi(value); err != nil {
					return detect.Profile{}, fmt.Errorf("%s:%d: radius %q: %w", path, lineNo, value, err)
				}
			case "x":
				if p.X, err = strconv.Atoi(value); err != nil {
					return detect.Profile{}, fmt.Errorf("%s:%d: x %q: %w", path, lineNo, value, err)
				}
			case "y":
				if p.Y, err = strconv.Atoi(value); err != nil {
					return detect.Profile{}, fmt.Errorf("%s:%d: y %q: %w", path, lineNo, value, err)
				}
			case "shift_x":
				if p.ShiftX, err = strconv.Atoi(value); err != nil {
					return detect.Profile{}, fmt.Errorf("%s:%d: shift_x %q: %w", path, lineNo, value, err)
				}
			case "shift_y":
				if p.ShiftY, err = strconv.Atoi(value); err != nil {
					return detect.Profile{}, fmt.Errorf("%s:%d: shift_y %q: %w", path, lineNo, value, err)
				}
			case "noise":
				if p.Noise, err = strconv.ParseFloat(value, 64); err != nil {
					return detect.Profile{}, fmt.Errorf("%s:%d: noise %q: %w", path, lineNo, value, err)
				}
			case "masked":
				if p.Masked, err = strconv.Atoi(value); err != nil {
					return detect.Profile{}, fmt.Errorf("%s:%d: masked %q: %w", path, lineNo, value, err)
				}
			case "kernel":
				fields := strings.Fields(value)
				if len(fields) > 0 {
					if side, err = parseKernelSide(fields[0]); err != nil {
						return detect.Profile{}, fmt.Errorf("%s:%d: %w", path, lineNo, err)
					}
				}
			}
			continue
		}

		fields := strings.Fields(line)
		row := make([]float64, len(fields))
		for i, field := range fields {
			v, err := strconv.ParseFloat(field, 64)
			if err != nil {
				return detect.Profile{}, fmt.Errorf("%s:%d: kernel sample %q: %w", path, lineNo, field, err)
			}
			if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				return detect.Profile{}, fmt.Errorf("%s:%d: kernel sample %q is negative or non-finite", path, lineNo, field)
			}
			row[i] = v
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return detect.Profile{}, fmt.Errorf("read %s: %w", path, err)
	}
	if version != "" && version != "1" {
		return detect.Profile{}, fmt.Errorf("%s: unsupported profile version %q", path, version)
	}
	if side == 0 {
		side = len(rows)
	}
	if p.Radius == 0 && side > 0 && side%2 == 1 {
		p.Radius = (side - 1) / 2
	}
	if p.Radius < 1 {
		return detect.Profile{}, fmt.Errorf("%s: profile has radius %d", path, p.Radius)
	}
	if side != 2*p.Radius+1 {
		return detect.Profile{}, fmt.Errorf("%s: kernel side %d does not match radius %d", path, side, p.Radius)
	}
	if len(rows) != side {
		return detect.Profile{}, fmt.Errorf("%s: kernel has %d rows, want %d", path, len(rows), side)
	}
	kernel := make([]float64, 0, side*side)
	var sum float64
	for row, values := range rows {
		if len(values) != side {
			return detect.Profile{}, fmt.Errorf("%s: kernel row %d has %d samples, want %d",
				path, row+1, len(values), side)
		}
		for _, v := range values {
			kernel = append(kernel, v)
			sum += v
		}
	}
	if sum <= 0 {
		return detect.Profile{}, fmt.Errorf("%s: kernel is empty", path)
	}
	for i := range kernel {
		kernel[i] /= sum
	}
	p.Kernel = kernel
	return p, nil
}

// parseKernelSide parses the "33x33" token from the kernel header.
func parseKernelSide(s string) (int, error) {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == 'x' || r == 'X' })
	if len(parts) != 2 {
		return 0, fmt.Errorf("kernel size %q is not WIDTHxHEIGHT", s)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("kernel width %q: %w", parts[0], err)
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("kernel height %q: %w", parts[1], err)
	}
	if w != h || w < 3 || w%2 == 0 {
		return 0, fmt.Errorf("kernel size %q must be a square odd number of at least 3", s)
	}
	return w, nil
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

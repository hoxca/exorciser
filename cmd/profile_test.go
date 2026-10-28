package cmd

import (
	"bufio"
	"context"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hoxca/exorciser/internal/detect"
)

func TestWriteProfilePNG(t *testing.T) {
	p := detect.Profile{
		Radius: 1,
		Kernel: []float64{
			0, 0, 0,
			0, 4, 0,
			0, 0, 0,
		},
	}
	path := filepath.Join(t.TempDir(), "profile.png")
	if err := writeProfilePNG(path, p); err != nil {
		t.Fatalf("writeProfilePNG: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := 3 * profilePNGScale
	if b := img.Bounds(); b.Dx() != want || b.Dy() != want {
		t.Fatalf("image is %d×%d, want %d×%d", b.Dx(), b.Dy(), want, want)
	}

	centre := profilePNGScale + profilePNGScale/2
	if got := grayAt(t, img, centre, centre); got != 255 {
		t.Errorf("centre = %d, want 255", got)
	}
	if got := grayAt(t, img, 0, 0); got != 0 {
		t.Errorf("corner = %d, want 0", got)
	}
}

func grayAt(t *testing.T, img image.Image, x, y int) uint8 {
	t.Helper()
	r, _, _, _ := img.At(x, y).RGBA()
	return uint8(r >> 8)
}

func TestWriteProfileNPE(t *testing.T) {
	p := detect.Profile{
		Radius: 1,
		X:      10,
		Y:      11,
		ShiftX: -1,
		ShiftY: 2,
		Noise:  0.0003,
		Masked: 7,
		Kernel: []float64{
			0, 0.5, 0,
			1, 2, 0,
			0, 0, 0,
		},
	}
	path := filepath.Join(t.TempDir(), "artefact.npe")
	if err := writeProfileNPE(path, p, "dirty.xisf"); err != nil {
		t.Fatalf("writeProfileNPE: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"# version: 1\n",
		"# dirty: dirty.xisf\n",
		"# x: 10\n",
		"# y: 11\n",
		"# shift_x: -1\n",
		"# shift_y: 2\n",
		"# radius: 1\n",
		"# noise: 0.0003\n",
		"# masked: 7\n",
		"# kernel: 3x3 row-major\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("file does not contain %q", want)
		}
	}

	var rows [][]float64
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		row := make([]float64, len(fields))
		for i, field := range fields {
			v, err := strconv.ParseFloat(field, 64)
			if err != nil {
				t.Fatalf("parse %q: %v", field, err)
			}
			row[i] = v
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d kernel rows, want 3", len(rows))
	}
	for y := range rows {
		if len(rows[y]) != 3 {
			t.Fatalf("row %d has %d values, want 3", y, len(rows[y]))
		}
		for x, got := range rows[y] {
			if want := p.Kernel[y*3+x]; got != want {
				t.Errorf("kernel[%d][%d] = %v, want %v", y, x, got, want)
			}
		}
	}
}

func TestReadArtefactProfileRoundTrip(t *testing.T) {
	p := detect.Profile{
		Radius: 1,
		X:      10,
		Y:      11,
		ShiftX: -1,
		ShiftY: 2,
		Noise:  0.0003,
		Masked: 7,
		Kernel: []float64{
			0, 0.5, 0,
			1, 2, 0,
			0, 0, 0,
		},
	}
	path := filepath.Join(t.TempDir(), "artefact.npe")
	if err := writeProfileNPE(path, p, "dirty.xisf"); err != nil {
		t.Fatalf("writeProfileNPE: %v", err)
	}

	got, err := readArtefactProfile(path)
	if err != nil {
		t.Fatalf("readArtefactProfile: %v", err)
	}
	if got.Radius != p.Radius || got.X != p.X || got.Y != p.Y ||
		got.ShiftX != p.ShiftX || got.ShiftY != p.ShiftY ||
		got.Noise != p.Noise || got.Masked != p.Masked {
		t.Errorf("metadata = %+v, want %+v", got, p)
	}
	if len(got.Kernel) != len(p.Kernel) {
		t.Fatalf("kernel has %d samples, want %d", len(got.Kernel), len(p.Kernel))
	}
	var sum float64
	for i, v := range got.Kernel {
		want := p.Kernel[i] / 3.5 // the writer stores the raw kernel; the reader normalises
		if math.Abs(v-want) > 1e-12 {
			t.Errorf("kernel[%d] = %v, want %v", i, v, want)
		}
		sum += v
	}
	if math.Abs(sum-1) > 1e-12 {
		t.Errorf("kernel sums to %v, want 1", sum)
	}
}

func TestReadArtefactProfileRejectsBadFile(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{
			name: "version",
			text: "# version: 2\n# radius: 1\n# kernel: 3x3\n1 0 0\n0 0 0\n0 0 0\n",
		},
		{
			name: "rows",
			text: "# version: 1\n# radius: 1\n# kernel: 3x3\n1 0 0\n0 0 0\n",
		},
		{
			name: "columns",
			text: "# version: 1\n# radius: 1\n# kernel: 3x3\n1 0\n0 0 0\n0 0 0\n",
		},
		{
			name: "negative",
			text: "# version: 1\n# radius: 1\n# kernel: 3x3\n1 0 0\n0 -1 0\n0 0 0\n",
		},
		{
			name: "radius",
			text: "# version: 1\n# radius: 2\n# kernel: 3x3\n1 0 0\n0 0 0\n0 0 0\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artefact.npe")
			if err := os.WriteFile(path, []byte(tt.text), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := readArtefactProfile(path); err == nil {
				t.Fatal("readArtefactProfile accepted a malformed profile")
			}
		})
	}
}

func TestWriteArtefactProfileRejectsBadKernel(t *testing.T) {
	p := detect.Profile{Radius: 2, Kernel: []float64{1, 2, 3}}
	if _, _, err := writeArtefactProfile(t.TempDir(), p, "dirty.xisf"); err == nil {
		t.Fatal("writeArtefactProfile accepted a kernel that does not match its radius")
	}
}

func TestRunDetectWritesArtefactProfile(t *testing.T) {
	const w, h = 256, 256
	const cx, cy = 128, 128

	root := t.TempDir()
	input := filepath.Join(root, "registered", "Light_FILTER-L_mono")
	if err := os.MkdirAll(input, 0o755); err != nil {
		t.Fatal(err)
	}

	sky := func(seed int64) []float32 {
		plane := eraseTestSky(w, h, 0.01, 0.0005, seed)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				plane[y*w+x] += float32(0.00001 * float64(x+y))
			}
		}
		return plane
	}
	ref := sky(1)
	refPath := filepath.Join(input, "ref.xisf")
	writeFloatXISF(t, refPath, w, h, ref)

	dirty := sky(2)
	addEraseGhost(dirty, w, cx, cy, 0.002, 4)
	dirtyPath := filepath.Join(input, "dirty.xisf")
	writeFloatXISF(t, dirtyPath, w, h, dirty)

	other := sky(3)
	otherPath := filepath.Join(input, "other.xisf")
	writeFloatXISF(t, otherPath, w, h, other)

	run := func(out string, withDirty bool) {
		t.Helper()
		opts := &detectOptions{
			input:         input,
			outputDir:     out,
			reference:     refPath,
			coord:         &point{x: cx, y: cy},
			window:        64,
			workers:       1,
			margin:        64,
			scales:        []float64{4, 8},
			minSNR:        6,
			profileRadius: 16,
		}
		if withDirty {
			opts.dirty = dirtyPath
			opts.dirtyCoord = &point{x: cx, y: cy}
		}
		if err := runDetect(context.Background(), []string{input}, opts); err != nil {
			t.Fatalf("runDetect: %v", err)
		}
	}

	out := filepath.Join(root, "out")
	run(out, true)
	pngPath := filepath.Join(out, "L", artefactProfilePNG)
	npePath := filepath.Join(out, "L", artefactProfileNPE)
	if _, err := os.Stat(pngPath); err != nil {
		t.Fatalf("%s missing: %v", pngPath, err)
	}
	if _, err := os.Stat(npePath); err != nil {
		t.Fatalf("%s missing: %v", npePath, err)
	}

	f, err := os.Open(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	_ = f.Close()
	if err != nil {
		t.Fatalf("decode profile PNG: %v", err)
	}
	want := (2*16 + 1) * profilePNGScale
	if b := img.Bounds(); b.Dx() != want || b.Dy() != want {
		t.Errorf("profile PNG is %d×%d, want %d×%d", b.Dx(), b.Dy(), want, want)
	}

	// Without --dirty no profile is exported.
	noDirty := filepath.Join(root, "out-nodirty")
	run(noDirty, false)
	for _, name := range []string{artefactProfilePNG, artefactProfileNPE} {
		if _, err := os.Stat(filepath.Join(noDirty, "L", name)); !os.IsNotExist(err) {
			t.Errorf("%s exists without --dirty: %v", name, err)
		}
	}
}

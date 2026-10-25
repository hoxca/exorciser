package xisfwrite_test

import (
	"path/filepath"
	"testing"

	"github.com/hoxca/exorciser/internal/xisfwrite"
	"github.com/hoxca/xisf"
)

func TestWriteRGB(t *testing.T) {
	const w, h = 4, 3
	pixels := make([]float32, 3*w*h)
	for i := range pixels {
		pixels[i] = float32(i) / 100
	}

	path := filepath.Join(t.TempDir(), "rgb.xisf")
	if err := xisfwrite.WriteRGB(path, "test", w, h, pixels); err != nil {
		t.Fatalf("WriteRGB: %v", err)
	}

	f, err := xisf.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	images := f.Images()
	if len(images) != 1 {
		t.Fatalf("got %d images, want 1", len(images))
	}
	im := images[0]
	if im.Width() != w || im.Height() != h {
		t.Errorf("image is %d×%d, want %d×%d", im.Width(), im.Height(), w, h)
	}
	if im.Channels() != 3 {
		t.Errorf("channels = %d, want 3", im.Channels())
	}
	if im.ColorSpace != xisf.RGB {
		t.Errorf("colour space = %v, want RGB", im.ColorSpace)
	}
	if im.SampleFormat != xisf.Float32 {
		t.Errorf("sample format = %v, want Float32", im.SampleFormat)
	}
	if im.PixelStorage != xisf.Normal {
		t.Errorf("pixel storage = %v, want Normal", im.PixelStorage)
	}
	if im.ID != "test" {
		t.Errorf("id = %q, want test", im.ID)
	}
	p, ok := im.Property("PCL:OverridableIdentifier")
	if !ok {
		t.Fatal("image has no PCL:OverridableIdentifier property, PixInsight would rename the view")
	}
	if p.Type != "Boolean" || p.Value != "false" {
		t.Errorf("PCL:OverridableIdentifier = %s:%s, want Boolean:false", p.Type, p.Value)
	}
	low, high, ok := im.RepresentableRange()
	if !ok || low != 0 || high != 1 {
		t.Errorf("representable range = %v:%v (ok=%v), want 0:1", low, high, ok)
	}

	for c := 0; c < 3; c++ {
		got, err := xisf.Plane[float32](im, c)
		if err != nil {
			t.Fatalf("plane %d: %v", c, err)
		}
		for i, v := range got {
			if v != pixels[c*w*h+i] {
				t.Fatalf("plane %d sample %d = %v, want %v", c, i, v, pixels[c*w*h+i])
			}
		}
	}
}

func TestWriteRGBRejectsWrongSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rgb.xisf")
	if err := xisfwrite.WriteRGB(path, "test", 4, 3, make([]float32, 10)); err == nil {
		t.Fatal("WriteRGB accepted a short pixel buffer")
	}
}

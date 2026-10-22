package cmd

import "testing"

func TestDigitGlyphs(t *testing.T) {
	if len(digitGlyphs) != 10 {
		t.Fatalf("got %d digit glyphs, want 10", len(digitGlyphs))
	}
	for digit, glyph := range digitGlyphs {
		filled := 0
		for y, row := range glyph {
			if len(row) != glyphWidth {
				t.Errorf("digit %d row %d is %d characters, want %d", digit, y, len(row), glyphWidth)
				continue
			}
			for x := 0; x < len(row); x++ {
				switch row[x] {
				case '#':
					filled++
				case ' ':
				default:
					t.Errorf("digit %d row %d column %d is %q, want '#' or ' '", digit, y, x, row[x])
				}
			}
		}
		if filled == 0 {
			t.Errorf("digit %d has no filled pixels", digit)
		}
	}
}

func TestLabelOrigin(t *testing.T) {
	tests := []struct {
		name         string
		cx, cy       int
		w, h         int
		arm, scale   int
		wantX, wantY int
	}{
		{name: "right", cx: 50, cy: 50, w: 100, h: 100, arm: 5, scale: 2, wantX: 57, wantY: 43},
		{name: "left", cx: 90, cy: 50, w: 100, h: 100, arm: 5, scale: 2, wantX: 49, wantY: 43},
		{name: "top", cx: 50, cy: 2, w: 100, h: 100, arm: 5, scale: 2, wantX: 57, wantY: 0},
		{name: "bottom", cx: 50, cy: 98, w: 100, h: 100, arm: 5, scale: 2, wantX: 57, wantY: 86},
		{name: "too wide", cx: 5, cy: 5, w: 20, h: 10, arm: 5, scale: 3, wantX: 0, wantY: 0},
	}

	if got, want := labelWidth("123", 2), 34; got != want {
		t.Fatalf("labelWidth(\"123\", 2) = %d, want %d", got, want)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, y := labelOrigin("123", tt.cx, tt.cy, tt.arm, tt.scale, tt.w, tt.h)
			if x != tt.wantX || y != tt.wantY {
				t.Errorf("labelOrigin = (%d,%d), want (%d,%d)", x, y, tt.wantX, tt.wantY)
			}
		})
	}
}

func TestDrawNumber(t *testing.T) {
	const w, h = 100, 100
	rgb := make([]float32, 3*w*h)
	drawNumber(rgb, w, h, 42, 50, 50, 5, 2)

	n := w * h
	red := 0
	for i := 0; i < n; i++ {
		if rgb[i] == 1 && rgb[n+i] == 0 && rgb[2*n+i] == 0 {
			red++
		}
	}
	want := (filledPixels(digitGlyphs[4]) + filledPixels(digitGlyphs[2])) * 2 * 2
	if red != want {
		t.Errorf("number drew %d red pixels, want %d", red, want)
	}
}

func TestDrawNumberZeroScale(t *testing.T) {
	const w, h = 32, 32
	rgb := make([]float32, 3*w*h)
	drawNumber(rgb, w, h, 1, 16, 16, 4, 0)
	for i, v := range rgb {
		if v != 0 {
			t.Fatalf("sample %d = %v, want an empty image", i, v)
		}
	}
}

func filledPixels(glyph [glyphHeight]string) int {
	n := 0
	for _, row := range glyph {
		for i := 0; i < len(row); i++ {
			if row[i] == '#' {
				n++
			}
		}
	}
	return n
}

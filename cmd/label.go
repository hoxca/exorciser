package cmd

import "strconv"

const (
	glyphWidth  = 5
	glyphHeight = 7
)

// digitGlyphs is a 5×7 bitmap font for the catalogue numbers. Each row is a
// string of glyphWidth characters, '#' for a filled pixel and ' ' otherwise.
var digitGlyphs = [10][glyphHeight]string{
	{
		" ### ",
		"#   #",
		"#  ##",
		"# # #",
		"##  #",
		"#   #",
		" ### ",
	},
	{
		"  #  ",
		" ##  ",
		"  #  ",
		"  #  ",
		"  #  ",
		"  #  ",
		" ### ",
	},
	{
		" ### ",
		"#   #",
		"    #",
		"   # ",
		"  #  ",
		" #   ",
		"#####",
	},
	{
		" ### ",
		"#   #",
		"    #",
		" ### ",
		"    #",
		"#   #",
		" ### ",
	},
	{
		"   # ",
		"  ## ",
		" # # ",
		"#  # ",
		"#####",
		"   # ",
		"   # ",
	},
	{
		"#####",
		"#    ",
		"#    ",
		"#### ",
		"    #",
		"#   #",
		" ### ",
	},
	{
		" ### ",
		"#    ",
		"#    ",
		"#### ",
		"#   #",
		"#   #",
		" ### ",
	},
	{
		"#####",
		"    #",
		"   # ",
		"  #  ",
		"  #  ",
		"  #  ",
		"  #  ",
	},
	{
		" ### ",
		"#   #",
		"#   #",
		" ### ",
		"#   #",
		"#   #",
		" ### ",
	},
	{
		" ### ",
		"#   #",
		"#   #",
		" ####",
		"    #",
		"    #",
		" ### ",
	},
}

var minusGlyph = [glyphHeight]string{
	"     ",
	"     ",
	"     ",
	"#####",
	"     ",
	"     ",
	"     ",
}

func glyphFor(ch byte) ([glyphHeight]string, bool) {
	if ch >= '0' && ch <= '9' {
		return digitGlyphs[ch-'0'], true
	}
	if ch == '-' {
		return minusGlyph, true
	}
	return [glyphHeight]string{}, false
}

// drawNumber prints number beside the cross at (cx, cy). Labels are drawn to
// the right when they fit and to the left otherwise, then clamped to the image.
func drawNumber(rgb []float32, w, h, number, cx, cy, arm, scale int) {
	if scale <= 0 {
		return
	}
	text := strconv.Itoa(number)
	x, y := labelOrigin(text, cx, cy, arm, scale, w, h)
	drawDigits(rgb, w, h, text, x, y, scale)
}

func labelWidth(text string, scale int) int {
	if text == "" || scale <= 0 {
		return 0
	}
	return len(text)*glyphWidth*scale + (len(text)-1)*scale
}

func labelOrigin(text string, cx, cy, arm, scale, w, h int) (int, int) {
	tw := labelWidth(text, scale)
	th := glyphHeight * scale
	gap := scale

	x := cx + arm + gap
	if x+tw > w {
		x = cx - arm - gap - tw
	}
	switch {
	case tw >= w:
		x = 0
	case x < 0:
		x = 0
	case x+tw > w:
		x = w - tw
	}

	y := cy - th/2
	switch {
	case th >= h:
		y = 0
	case y < 0:
		y = 0
	case y+th > h:
		y = h - th
	}
	return x, y
}

func drawDigits(rgb []float32, w, h int, text string, x0, y0, scale int) {
	if scale <= 0 {
		return
	}
	planeSize := w * h
	advance := glyphWidth*scale + scale
	for i := 0; i < len(text); i++ {
		glyph, ok := glyphFor(text[i])
		if !ok {
			continue
		}
		gx0 := x0 + i*advance
		for gy, row := range glyph {
			for gx := 0; gx < glyphWidth && gx < len(row); gx++ {
				if row[gx] != '#' {
					continue
				}
				px0, px1, okX := pixelSpan(gx0+gx*scale, 0, scale, w)
				py0, py1, okY := pixelSpan(y0+gy*scale, 0, scale, h)
				if okX && okY {
					fillRed(rgb, planeSize, w, px0, px1, py0, py1)
				}
			}
		}
	}
}

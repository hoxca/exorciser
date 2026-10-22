// Package report writes the per-image detection table as CSV.
//
// The table targets a French locale: fields are separated by a semicolon and
// decimals use a comma, so a spreadsheet opens the numbers as numbers instead
// of text. encoding/csv still reads the file structurally, since commas are
// ordinary characters when the separator is a semicolon.
package report

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"
)

// Separator is the CSV field delimiter.
const Separator = ';'

// DecimalSeparator replaces the '.' of formatted floats.
const DecimalSeparator = ','

// SNRDecimals is the number of decimals written for peak_snr. The other
// statistic, peak_amp, is in pixel units and typically ~1e-4, so it uses the
// shortest round-trip form instead.
const SNRDecimals = 3

// Header is the CSV column order. It matches Row.Fields.
var Header = []string{
	"file",
	"detected",
	"width",
	"height",
	"win_x",
	"win_y",
	"win_size",
	"norm_a",
	"norm_b",
	"noise",
	"ghost_sigma",
	"peak_x",
	"peak_y",
	"peak_snr",
	"peak_amp",
	"clipped_px",
	"duration_ms",
	"error",
}

// Row is one image's line in the detection table. The image is compared to a
// reference frame through the affine transform frame ≈ NormA*reference + NormB;
// Noise is the residual noise of the background ring, GhostSigma the
// matched-filter scale with the strongest response, and PeakSNR its
// significance. Detected is true when PeakSNR reaches the caller's threshold.
//
// When Err is set every measured value is left empty, since none of them can be
// trusted. Unknown dimensions are also empty.
type Row struct {
	File   string
	Width  int
	Height int

	// Detected is the report's verdict: the strongest peak in the window is
	// significant enough to be called a ghost.
	Detected bool

	WinX    int
	WinY    int
	WinSize int

	NormA      float64
	NormB      float64
	Noise      float64
	GhostSigma float64

	PeakX   int
	PeakY   int
	PeakSNR float64
	PeakAmp float64

	Clipped int

	DurationMS int64
	Err        string
}

// Fields returns r as a CSV record in Header order.
func (r Row) Fields() []string {
	failed := r.Err != ""

	dim := func(v int) string {
		if v == 0 {
			return ""
		}
		return strconv.Itoa(v)
	}
	count := func(v int) string {
		if failed {
			return ""
		}
		return strconv.Itoa(v)
	}
	stat := func(v float64) string {
		if failed {
			return ""
		}
		return formatFloat(v, -1)
	}
	scaled := func(v float64) string {
		if failed {
			return ""
		}
		return formatFloat(v, SNRDecimals)
	}
	flag := func(v bool) string {
		if failed {
			return ""
		}
		return strconv.FormatBool(v)
	}

	return []string{
		r.File,
		flag(r.Detected),
		dim(r.Width),
		dim(r.Height),
		count(r.WinX),
		count(r.WinY),
		count(r.WinSize),
		stat(r.NormA),
		stat(r.NormB),
		stat(r.Noise),
		stat(r.GhostSigma),
		count(r.PeakX),
		count(r.PeakY),
		scaled(r.PeakSNR),
		stat(r.PeakAmp),
		count(r.Clipped),
		strconv.FormatInt(r.DurationMS, 10),
		r.Err,
	}
}

// Write writes the header and rows to w as semicolon-delimited CSV.
func Write(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	cw.Comma = Separator
	if err := cw.Write(Header); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write(r.Fields()); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// formatFloat renders v with a comma as decimal separator. 'f' never uses an
// exponent, so the only '.' to replace is the decimal point. A negative
// precision keeps the shortest representation that round-trips.
func formatFloat(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	return strings.Replace(s, ".", string(DecimalSeparator), 1)
}

package report

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// BackgroundHeader is the column order of the background window record.
var BackgroundHeader = []string{
	"filter",
	"x",
	"y",
	"size",
	"mean",
	"noise",
	"snr",
	"pixels",
}

// Background is the sky window chosen by union. X and Y are the window centre
// in registered pixels, Mean is the sky level inside the window, Noise its
// standard deviation, and SNR their ratio. Pixels is how many samples survived
// the star mask and clipping; the statistics are computed from those only.
type Background struct {
	Filter string
	X      int
	Y      int
	Size   int
	Mean   float64
	Noise  float64
	SNR    float64
	Pixels int
}

// Fields returns b as a CSV record in BackgroundHeader order.
func (b Background) Fields() []string {
	return []string{
		b.Filter,
		strconv.Itoa(b.X),
		strconv.Itoa(b.Y),
		strconv.Itoa(b.Size),
		formatFloat(b.Mean, -1),
		formatFloat(b.Noise, -1),
		formatFloat(b.SNR, SNRDecimals),
		strconv.Itoa(b.Pixels),
	}
}

// ReadBackground parses the single row written by WriteBackground. Columns are
// matched by name, so their order does not matter; filter, x, y and size are
// required, the statistics are optional.
func ReadBackground(r io.Reader) (Background, error) {
	cr := csv.NewReader(r)
	cr.Comma = Separator
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return Background{}, fmt.Errorf("read header: %w", err)
	}
	index := make(map[string]int, len(header))
	for i, name := range header {
		index[strings.TrimSpace(name)] = i
	}
	for _, name := range []string{"filter", "x", "y", "size"} {
		if _, ok := index[name]; !ok {
			return Background{}, fmt.Errorf("background table has no %q column", name)
		}
	}

	rec, err := cr.Read()
	if err != nil {
		return Background{}, fmt.Errorf("read row: %w", err)
	}
	cell := func(name string) string {
		i, ok := index[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	b := Background{Filter: cell("filter")}
	values := []struct {
		name string
		dst  *int
	}{
		{"x", &b.X},
		{"y", &b.Y},
		{"size", &b.Size},
		{"pixels", &b.Pixels},
	}
	for _, v := range values {
		raw := cell(v.name)
		if raw == "" {
			continue
		}
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return Background{}, fmt.Errorf("%s %q: %w", v.name, raw, err)
		}
		*v.dst = parsed
	}
	stats := []struct {
		name string
		dst  *float64
	}{
		{"mean", &b.Mean},
		{"noise", &b.Noise},
		{"snr", &b.SNR},
	}
	for _, v := range stats {
		parsed, err := parseCellFloat(cell(v.name))
		if err != nil {
			return Background{}, fmt.Errorf("%s: %w", v.name, err)
		}
		*v.dst = parsed
	}
	if b.Filter == "" {
		return Background{}, errors.New("background table has an empty filter")
	}
	return b, nil
}

// WriteBackground writes the header and one background row as
// semicolon-delimited CSV with a decimal comma.
func WriteBackground(w io.Writer, b Background) error {
	cw := csv.NewWriter(w)
	cw.Comma = Separator
	if err := cw.Write(BackgroundHeader); err != nil {
		return err
	}
	if err := cw.Write(b.Fields()); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

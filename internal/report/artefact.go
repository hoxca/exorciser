package report

import (
	"encoding/csv"
	"io"
	"strconv"
)

// ArtefactHeader is the column order of the blind-scan catalogue.
var ArtefactHeader = []string{
	"n",
	"x",
	"y",
	"snr",
	"amp",
	"ghost_sigma",
}

// Artefact is one diffuse feature found in the stacked difference image. N is
// its rank by |SNR|, starting at one. X and Y are 0-based registered pixels.
type Artefact struct {
	N     int
	X     int
	Y     int
	SNR   float64
	Amp   float64
	Scale float64
}

// Fields returns a as a CSV record in ArtefactHeader order.
func (a Artefact) Fields() []string {
	return []string{
		strconv.Itoa(a.N),
		strconv.Itoa(a.X),
		strconv.Itoa(a.Y),
		formatFloat(a.SNR, SNRDecimals),
		formatFloat(a.Amp, -1),
		formatFloat(a.Scale, -1),
	}
}

// WriteArtefacts writes the header and artefacts to w as semicolon-delimited
// CSV with a decimal comma.
func WriteArtefacts(w io.Writer, artefacts []Artefact) error {
	cw := csv.NewWriter(w)
	cw.Comma = Separator
	if err := cw.Write(ArtefactHeader); err != nil {
		return err
	}
	for _, a := range artefacts {
		if err := cw.Write(a.Fields()); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

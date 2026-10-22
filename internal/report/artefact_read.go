package report

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ReadArtefacts parses a catalogue written by WriteArtefacts. Columns are
// matched by name, so their order does not matter and extra columns are
// ignored. The rank and position are required; the statistics are optional.
func ReadArtefacts(r io.Reader) ([]Artefact, error) {
	cr := csv.NewReader(r)
	cr.Comma = Separator
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	index := make(map[string]int, len(header))
	for i, name := range header {
		index[strings.TrimSpace(name)] = i
	}
	for _, name := range []string{"n", "x", "y"} {
		if _, ok := index[name]; !ok {
			return nil, fmt.Errorf("artefact table has no %q column", name)
		}
	}

	var artefacts []Artefact
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		a, err := parseArtefact(rec, index)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		artefacts = append(artefacts, a)
	}
	return artefacts, nil
}

func parseArtefact(rec []string, index map[string]int) (Artefact, error) {
	cell := func(name string) string {
		i, ok := index[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	anInt := func(name string) (int, error) {
		raw := cell(name)
		if raw == "" {
			return 0, fmt.Errorf("empty %s", name)
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("%s %q: %w", name, raw, err)
		}
		return v, nil
	}

	n, err := anInt("n")
	if err != nil {
		return Artefact{}, err
	}
	x, err := anInt("x")
	if err != nil {
		return Artefact{}, err
	}
	y, err := anInt("y")
	if err != nil {
		return Artefact{}, err
	}

	a := Artefact{N: n, X: x, Y: y}
	values := []struct {
		name string
		dst  *float64
	}{
		{"snr", &a.SNR},
		{"amp", &a.Amp},
		{"ghost_sigma", &a.Scale},
	}
	for _, v := range values {
		parsed, err := parseCellFloat(cell(v.name))
		if err != nil {
			return Artefact{}, fmt.Errorf("%s: %w", v.name, err)
		}
		*v.dst = parsed
	}
	return a, nil
}

package report

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Read parses a detection table written by Write. Columns are matched by name,
// so their order does not matter and extra columns are ignored. A row whose
// error column is set carries only File and Err; its numeric fields read as
// zero, exactly as Write rendered them.
func Read(r io.Reader) ([]Row, error) {
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
	for _, name := range []string{"file", "detected", "peak_snr"} {
		if _, ok := index[name]; !ok {
			return nil, fmt.Errorf("report has no %q column", name)
		}
	}

	var rows []Row
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		row, err := parseRow(rec, index)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseRow(rec []string, index map[string]int) (Row, error) {
	cell := func(name string) string {
		i, ok := index[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	row := Row{File: cell("file"), Err: cell("error")}
	row.Kernel = cell("kernel")
	if row.File == "" {
		return Row{}, errors.New("empty file name")
	}

	if detected := cell("detected"); detected != "" {
		v, err := strconv.ParseBool(detected)
		if err != nil {
			return Row{}, fmt.Errorf("detected %q: %w", detected, err)
		}
		row.Detected = v
	}

	values := []struct {
		name string
		dst  *float64
	}{
		{"norm_a", &row.NormA},
		{"norm_b", &row.NormB},
		{"noise", &row.Noise},
		{"ghost_sigma", &row.GhostSigma},
		{"peak_snr", &row.PeakSNR},
		{"peak_amp", &row.PeakAmp},
	}
	for _, v := range values {
		parsed, err := parseCellFloat(cell(v.name))
		if err != nil {
			return Row{}, fmt.Errorf("%s: %w", v.name, err)
		}
		*v.dst = parsed
	}

	counts := []struct {
		name string
		dst  *int
	}{
		{"width", &row.Width},
		{"height", &row.Height},
		{"win_x", &row.WinX},
		{"win_y", &row.WinY},
		{"win_size", &row.WinSize},
		{"peak_x", &row.PeakX},
		{"peak_y", &row.PeakY},
		{"clipped_px", &row.Clipped},
	}
	for _, v := range counts {
		parsed, err := parseCellInt(cell(v.name))
		if err != nil {
			return Row{}, fmt.Errorf("%s: %w", v.name, err)
		}
		*v.dst = parsed
	}

	if raw := cell("duration_ms"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Row{}, fmt.Errorf("duration_ms: %w", err)
		}
		row.DurationMS = v
	}
	return row, nil
}

func parseCellFloat(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(strings.ReplaceAll(s, string(DecimalSeparator), "."), 64)
}

func parseCellInt(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

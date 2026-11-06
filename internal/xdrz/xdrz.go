// Package xdrz reads PixInsight XML Drizzle Data Format sidecars.
package xdrz

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// Data is the part of an XDRZ sidecar needed to map registered coordinates to
// source-image coordinates.
type Data struct {
	SourceImage          string
	AlignmentTargetImage string
	Matrix               [9]float64
}

type document struct {
	SourceImage          string `xml:"SourceImage"`
	AlignmentTargetImage string `xml:"AlignmentTargetImage"`
	AlignmentMatrix      string `xml:"AlignmentMatrix"`
}

// Read parses the source path and 3x3 alignment matrix.
//
// The matrix maps alignment-target (registered) coordinates to source
// coordinates using 0-based pixel coordinates. Use ToTarget for the inverse.
func Read(path string) (Data, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Data{}, err
	}
	var doc document
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return Data{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(doc.SourceImage) == "" {
		return Data{}, fmt.Errorf("%s has no SourceImage", path)
	}
	fields := strings.Split(doc.AlignmentMatrix, ",")
	if len(fields) != 9 {
		return Data{}, fmt.Errorf("%s has %d matrix values, want 9", path, len(fields))
	}
	var m [9]float64
	for i, field := range fields {
		v, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return Data{}, fmt.Errorf("%s matrix value %q is not finite", path, field)
		}
		m[i] = v
	}
	det := m[0]*m[4] - m[1]*m[3]
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return Data{}, fmt.Errorf("%s alignment matrix is singular", path)
	}
	return Data{
		SourceImage:          doc.SourceImage,
		AlignmentTargetImage: doc.AlignmentTargetImage,
		Matrix:               m,
	}, nil
}

// ToSource maps a registered coordinate to the source image.
func (d Data) ToSource(x, y float64) (float64, float64) {
	return d.Matrix[0]*x + d.Matrix[1]*y + d.Matrix[2],
		d.Matrix[3]*x + d.Matrix[4]*y + d.Matrix[5]
}

// ToTarget maps a source coordinate back to the registered image.
func (d Data) ToTarget(x, y float64) (float64, float64) {
	m := d.Matrix
	det := m[0]*m[4] - m[1]*m[3]
	inv := [9]float64{
		m[4] / det,
		-m[1] / det,
		(m[1]*m[5] - m[4]*m[2]) / det,
		-m[3] / det,
		m[0] / det,
		(m[3]*m[2] - m[0]*m[5]) / det,
	}
	return inv[0]*x + inv[1]*y + inv[2],
		inv[3]*x + inv[4]*y + inv[5]
}

// CopyWithSource copies an XDRZ file and replaces its SourceImage text.
func CopyWithSource(src, dst, source string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	open := []byte("<SourceImage>")
	close := []byte("</SourceImage>")
	start := bytes.Index(raw, open)
	if start < 0 {
		return errors.New("xdrz has no SourceImage")
	}
	start += len(open)
	end := bytes.Index(raw[start:], close)
	if end < 0 {
		return errors.New("xdrz has an unterminated SourceImage")
	}
	end += start
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(source)); err != nil {
		return err
	}
	out := make([]byte, 0, len(raw)+escaped.Len())
	out = append(out, raw[:start]...)
	out = append(out, escaped.Bytes()...)
	out = append(out, raw[end:]...)
	return os.WriteFile(dst, out, 0o644)
}

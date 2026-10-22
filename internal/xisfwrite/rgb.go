// Package xisfwrite writes XISF images that the read-only xisf package cannot
// construct, notably three-channel RGB images.
package xisfwrite

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"math"
	"os"
	"time"

	"github.com/hoxca/xisf"
)

const attachmentAlignment = 4096

// WriteRGB writes pixels as a zlib-compressed Float32 RGB XISF image.
//
// len(pixels) must be 3*w*h. The first w*h samples are the red channel, the
// next w*h are green, and the last w*h are blue, each in row-major order. The
// samples are stored pixel by pixel, with the three channels consecutive. The
// file declares bounds="0:1", which XISF requires for floating-point images.
func WriteRGB(path, id string, w, h int, pixels []float32) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("image is %d×%d, want positive dimensions", w, h)
	}
	if len(pixels) != 3*w*h {
		return fmt.Errorf("got %d samples, want %d for %d×%d RGB", len(pixels), 3*w*h, w, h)
	}
	if id == "" {
		return errors.New("image id is empty")
	}

	stored, err := compress(pixels)
	if err != nil {
		return err
	}
	rawSize := int64(len(pixels)) * 4
	header, dataOffset := buildHeader(id, w, h, int64(len(stored)), rawSize)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := writeFile(f, header, dataOffset, stored); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

func compress(pixels []float32) ([]byte, error) {
	var out bytes.Buffer
	zw, err := zlib.NewWriterLevel(&out, zlib.BestSpeed)
	if err != nil {
		return nil, err
	}

	planeSize := len(pixels) / 3
	const chunkSamples = 16 * 1024
	buf := make([]byte, chunkSamples*4)
	n := 0
	for i := 0; i < planeSize; i++ {
		for c := 0; c < 3; c++ {
			binary.LittleEndian.PutUint32(buf[n:], math.Float32bits(pixels[c*planeSize+i]))
			n += 4
			if n == len(buf) {
				if _, err := zw.Write(buf); err != nil {
					_ = zw.Close()
					return nil, fmt.Errorf("compress pixels: %w", err)
				}
				n = 0
			}
		}
	}
	if n > 0 {
		if _, err := zw.Write(buf[:n]); err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("compress pixels: %w", err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finish compression: %w", err)
	}
	return out.Bytes(), nil
}

func buildHeader(id string, w, h int, storedSize, rawSize int64) ([]byte, int64) {
	var offset int64
	for {
		header := headerXML(id, w, h, offset, storedSize, rawSize)
		next := alignUp(int64(xisf.PreambleLen+len(header)), attachmentAlignment)
		if next == offset {
			return []byte(header), offset
		}
		offset = next
	}
}

func headerXML(id string, w, h int, offset, storedSize, rawSize int64) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<xisf version="1.0" xmlns="http://www.pixinsight.com/xisf">` +
		fmt.Sprintf(
			`<Image id="%s" geometry="%d:%d:3" sampleFormat="Float32" bounds="0:1" `+
				`colorSpace="RGB" pixelStorage="Normal" location="attachment:%d:%d" `+
				`imageType="MasterLight" compression="zlib:%d"/>`,
			html.EscapeString(id), w, h, offset, storedSize, rawSize,
		) +
		`<Metadata>` +
		fmt.Sprintf(`<Property id="XISF:CreationTime" type="String">%s</Property>`,
			time.Now().UTC().Format(time.RFC3339)) +
		`<Property id="XISF:CreatorApplication" type="String">exorciser</Property>` +
		`</Metadata>` +
		`</xisf>`
}

func alignUp(n int64, alignment int64) int64 {
	return (n + alignment - 1) &^ (alignment - 1)
}

func writeFile(f *os.File, header []byte, dataOffset int64, stored []byte) error {
	if int64(len(header))+xisf.PreambleLen > dataOffset {
		return errors.New("header overlaps the image data")
	}
	if len(header) > math.MaxUint32 {
		return errors.New("header is too large")
	}

	var preamble [xisf.PreambleLen]byte
	copy(preamble[:8], "XISF0100")
	binary.LittleEndian.PutUint32(preamble[8:12], uint32(len(header)))

	if _, err := f.Write(preamble[:]); err != nil {
		return fmt.Errorf("write preamble: %w", err)
	}
	if _, err := f.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	padding := dataOffset - int64(xisf.PreambleLen+len(header))
	if padding > 0 {
		if _, err := f.Write(make([]byte, padding)); err != nil {
			return fmt.Errorf("write alignment padding: %w", err)
		}
	}
	if _, err := f.Write(stored); err != nil {
		return fmt.Errorf("write pixels: %w", err)
	}
	return nil
}

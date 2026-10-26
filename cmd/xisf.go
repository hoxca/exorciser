package cmd

import (
	"fmt"

	"github.com/hoxca/xisf"
)

// writeRGBImage writes pixels as a zlib-compressed Float32 RGB XISF image.
//
// len(pixels) must be 3*w*h. The first w*h samples are the red channel, the
// next w*h are green, and the last w*h are blue, each in row-major order. The
// samples are stored pixel by pixel, with the three channels consecutive.
//
// The XISF image id is kept as the PixInsight window name by an image
// property, so a window opened from the file is not renamed after imageType.
func writeRGBImage(path, id string, w, h int, pixels []float32) error {
	f, err := xisf.NewImage(xisf.ImageSpec{
		Width:        w,
		Height:       h,
		Channels:     3,
		ColorSpace:   xisf.RGB,
		PixelStorage: xisf.Normal,
		ID:           id,
		ImageType:    "MasterLight",
	}, pixels)
	if err != nil {
		return err
	}
	im := f.Images()[0]
	if err := im.AddProperty(xisf.Property{
		ID:    "PCL:OverridableIdentifier",
		Type:  "Boolean",
		Value: "false",
	}); err != nil {
		return fmt.Errorf("add view name property: %w", err)
	}
	return f.WriteFile(path, &xisf.WriteOptions{
		UpdateCreationTime: true,
		CreatorApplication: "exorciser",
		Compression: &xisf.Compression{
			Codec: xisf.CodecZlib,
		},
		// The abstract level 10 maps to zlib level 1, the previous
		// writer's BestSpeed.
		CompressionLevel: 10,
	})
}

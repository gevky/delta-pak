// Package statepreview converts save-state screenshots between the dimensions
// used by NextUI and Delta. Both sides store PNG data; NextUI merely gives the
// file a historical .bmp suffix.
package statepreview

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

const (
	deltaWidth   = 256
	deltaHeight  = 224
	nextUIWidth  = 1024
	nextUIHeight = 768
)

// ToDelta returns a native-size PNG suitable for Delta's thumbnail sidecar.
func ToDelta(data []byte) ([]byte, error) {
	return resizePNG(data, deltaWidth, deltaHeight)
}

// ToNextUI returns a full-screen PNG suitable for NextUI's slot preview.
func ToNextUI(data []byte) ([]byte, error) {
	return resizePNG(data, nextUIWidth, nextUIHeight)
}

func resizePNG(data []byte, width, height int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode preview: %w", err)
	}
	bounds := src.Bounds()
	if bounds.Dx() == 0 || bounds.Dy() == 0 {
		return nil, fmt.Errorf("decode preview: image has no pixels")
	}

	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		sy := bounds.Min.Y + y*bounds.Dy()/height
		for x := 0; x < width; x++ {
			sx := bounds.Min.X + x*bounds.Dx()/width
			c := color.NRGBAModel.Convert(src.At(sx, sy)).(color.NRGBA)
			c.A = 255
			dst.SetNRGBA(x, y, c)
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, fmt.Errorf("encode preview: %w", err)
	}
	return out.Bytes(), nil
}

package statepreview

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	img.Set(1, 0, color.NRGBA{G: 255, A: 255})
	img.Set(0, 1, color.NRGBA{B: 255, A: 255})
	img.Set(1, 1, color.NRGBA{R: 255, G: 255, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func assertPNGSizeAndCorners(t *testing.T, data []byte, width, height int) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode converted preview: %v", err)
	}
	if format != "png" {
		t.Fatalf("format = %q, want png", format)
	}
	if got := img.Bounds().Size(); got.X != width || got.Y != height {
		t.Fatalf("size = %dx%d, want %dx%d", got.X, got.Y, width, height)
	}
	want := []color.NRGBA{
		{R: 255, A: 255},
		{G: 255, A: 255},
		{B: 255, A: 255},
		{R: 255, G: 255, A: 255},
	}
	points := []image.Point{{0, 0}, {width - 1, 0}, {0, height - 1}, {width - 1, height - 1}}
	for i, p := range points {
		if got := color.NRGBAModel.Convert(img.At(p.X, p.Y)).(color.NRGBA); got != want[i] {
			t.Errorf("pixel %v = %#v, want %#v", p, got, want[i])
		}
	}
}

func TestToDeltaCreatesNativeSNESThumbnail(t *testing.T) {
	got, err := ToDelta(testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	assertPNGSizeAndCorners(t, got, 256, 224)
}

func TestToNextUICreatesBrickPreview(t *testing.T) {
	got, err := ToNextUI(testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	assertPNGSizeAndCorners(t, got, 1024, 768)
}

func TestMalformedPreviewIsRejected(t *testing.T) {
	if _, err := ToDelta([]byte("not an image")); err == nil {
		t.Fatal("malformed preview was accepted")
	}
}

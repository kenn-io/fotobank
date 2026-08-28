package thumb_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/thumb"
)

func TestDecodeJPEGNoOrientation(t *testing.T) {
	r := require.New(t)
	bs, err := os.ReadFile(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	r.NoError(err)
	ref, err := jpeg.Decode(bytes.NewReader(bs))
	r.NoError(err)

	img, err := thumb.Decode("image/jpeg", bytes.NewReader(bs))
	r.NoError(err)
	r.Equal(ref.Bounds().Dx(), img.Bounds().Dx())
	r.Equal(ref.Bounds().Dy(), img.Bounds().Dy())
}

func TestDecodeJPEGOrientation6RotatesDimensions(t *testing.T) {
	// Orientation=6 means "rotate 90° CW" — a 100×200 pixel buffer
	// displays as 200×100. Decode must apply the rotation so downstream
	// resize/encode sees display-correct pixels AND dimensions.
	r := require.New(t)
	path := filepath.Join("..", "..", "testdata", "thumb", "portrait-orient6.jpg")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip("portrait-orient6.jpg fixture unavailable — see Task 4 notes")
	}
	bs, err := os.ReadFile(path)
	r.NoError(err)
	img, err := thumb.Decode("image/jpeg", bytes.NewReader(bs))
	r.NoError(err)

	// Dimensions first.
	r.Equal(200, img.Bounds().Dx(), "orientation=6 should swap W/H")
	r.Equal(100, img.Bounds().Dy())

	// Pixel content: the fixture's gradient is
	//   src(sx, sy) = RGBA{200, sy*255/200, sx*255/100, 255}
	// After 90° CW, source (sx, sy) -> display (h-1-sy, sx) = (199-sy, sx).
	// So display(199, 0) comes from source (0, 0) -> R=200, G=0, B=0.
	// Display(0, 99) comes from source (99, 199) -> R=200, G=~253, B=~252.
	// JPEG + YCbCr conversion introduces small rounding, so we assert
	// approximately (within 12) rather than exactly — enough to distinguish
	// the correct rotation from the broken one (which produced pure green
	// at display(199,0) — wildly off).
	corner := img.At(199, 0)
	rC, gC, bC, _ := corner.RGBA()
	r.InDelta(200, int(rC>>8), 12, "display(199,0).R")
	r.InDelta(0, int(gC>>8), 12, "display(199,0).G")
	r.InDelta(0, int(bC>>8), 12, "display(199,0).B")

	far := img.At(0, 99)
	rF, gF, bF, _ := far.RGBA()
	r.InDelta(200, int(rF>>8), 12, "display(0,99).R")
	r.InDelta(253, int(gF>>8), 12, "display(0,99).G")
	r.InDelta(252, int(bF>>8), 12, "display(0,99).B")
}

func TestDecodeHEICReturnsErrNoPreview(t *testing.T) {
	// HEIC returns no_preview without reading src.
	_, err := thumb.Decode("image/heic", bytes.NewReader(nil))
	require.ErrorIs(t, err, thumb.ErrNoPreview)
}

func TestDecodeHEIFReturnsErrNoPreview(t *testing.T) {
	_, err := thumb.Decode("image/heif", bytes.NewReader(nil))
	require.ErrorIs(t, err, thumb.ErrNoPreview)
}

func TestDecodeUnknownMIMERejected(t *testing.T) {
	_, err := thumb.Decode("application/octet-stream", bytes.NewReader(nil))
	require.Error(t, err)
	require.NotErrorIs(t, err, thumb.ErrNoPreview)
}

func TestDecodePNGReturnsImage(t *testing.T) {
	// Ingest classifies .png as TypePhoto + "image/png" (see
	// internal/ingest/discover.go), so the worker must decode it
	// rather than surfacing "unsupported mime".
	r := require.New(t)
	src := image.NewRGBA(image.Rect(0, 0, 16, 24))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	r.NoError(png.Encode(&buf, src))

	img, err := thumb.Decode("image/png", bytes.NewReader(buf.Bytes()))
	r.NoError(err)
	r.Equal(16, img.Bounds().Dx())
	r.Equal(24, img.Bounds().Dy())
}

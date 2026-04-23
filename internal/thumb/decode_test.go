package thumb_test

import (
	"bytes"
	"errors"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/thumb"
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
	// Orientation=6 means "rotate 90 CW" — a 100x200 pixel buffer
	// displays as 200x100. Decode must apply the rotation so downstream
	// resize/encode sees display-correct dimensions.
	r := require.New(t)
	path := filepath.Join("..", "..", "testdata", "thumb", "portrait-orient6.jpg")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip("portrait-orient6.jpg fixture unavailable — see Task 4 notes")
	}
	bs, err := os.ReadFile(path)
	r.NoError(err)
	img, err := thumb.Decode("image/jpeg", bytes.NewReader(bs))
	r.NoError(err)
	r.Equal(200, img.Bounds().Dx(), "orientation=6 should swap W/H")
	r.Equal(100, img.Bounds().Dy())
}

func TestDecodeHEICReturnsErrNoPreview(t *testing.T) {
	// HEIC -> no_preview per spec §11.4. Decode does not touch src.
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

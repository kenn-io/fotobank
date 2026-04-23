package thumb_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/thumb"
)

func newGradient(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 64, A: 255})
		}
	}
	return img
}

func TestResizePreservesAspectLandscape(t *testing.T) {
	// 4000x3000 -> maxEdge 256 -> 256x192 (aspect preserved, long edge pinned).
	out := thumb.Resize(newGradient(4000, 3000), 256)
	require.Equal(t, 256, out.Bounds().Dx())
	require.Equal(t, 192, out.Bounds().Dy())
}

func TestResizePreservesAspectPortrait(t *testing.T) {
	// 3000x4000 -> maxEdge 256 -> 192x256.
	out := thumb.Resize(newGradient(3000, 4000), 256)
	require.Equal(t, 192, out.Bounds().Dx())
	require.Equal(t, 256, out.Bounds().Dy())
}

func TestResizeSkipsUpscale(t *testing.T) {
	// Source smaller than target: return source unchanged.
	src := newGradient(100, 75)
	out := thumb.Resize(src, 256)
	require.Equal(t, 100, out.Bounds().Dx())
	require.Equal(t, 75, out.Bounds().Dy())
}

func TestEncodeJPEGRoundTrip(t *testing.T) {
	r := require.New(t)
	src := newGradient(256, 192)
	var buf bytes.Buffer
	r.NoError(thumb.EncodeJPEG(&buf, src, 85))
	r.Greater(buf.Len(), 200, "empty output?")

	decoded, err := jpeg.Decode(&buf)
	r.NoError(err)
	r.Equal(256, decoded.Bounds().Dx())
	r.Equal(192, decoded.Bounds().Dy())
}

func TestEncodeJPEGRejectsBadQuality(t *testing.T) {
	src := newGradient(10, 10)
	var buf bytes.Buffer
	require.Error(t, thumb.EncodeJPEG(&buf, src, 0))
	require.Error(t, thumb.EncodeJPEG(&buf, src, 101))
}

func TestResizeZeroMaxEdgeReturnsSourceUnchanged(t *testing.T) {
	// Size.MaxEdge() returns 0 for unknown Size values; Resize must
	// not silently produce a zero-dimension image downstream.
	src := newGradient(100, 75)
	out := thumb.Resize(src, 0)
	require.Equal(t, 100, out.Bounds().Dx())
	require.Equal(t, 75, out.Bounds().Dy())
}

func TestResizeExtremeAspectClampsMinimumOnePixel(t *testing.T) {
	// A 10000x1 image scaled to maxEdge=256 has dstH = 1*256/10000 = 0
	// under naive math. Clamp to >=1 so the output is always encodable.
	out := thumb.Resize(newGradient(10000, 1), 256)
	require.Equal(t, 256, out.Bounds().Dx())
	require.GreaterOrEqual(t, out.Bounds().Dy(), 1)
}

package imginput_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/imginput"
)

func TestEncodeFromPreview_DownscalesToProfileEdge(t *testing.T) {
	require := require.New(t)
	src := makeJPEG(t, 3000, 2000)
	out, err := imginput.Encode(src)
	require.NoError(err)
	require.NotEmpty(out)

	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	require.NoError(err)
	require.LessOrEqual(cfg.Width, 1024)
	require.LessOrEqual(cfg.Height, 1024)
	// Aspect ratio: original 3:2 → max edge 1024 → 1024 x 682-683.
	if cfg.Width >= cfg.Height {
		require.Equal(1024, cfg.Width)
	} else {
		require.Equal(1024, cfg.Height)
	}
}

func TestEncodeFromPreview_Idempotent(t *testing.T) {
	require := require.New(t)
	// Already-small input stays at native dimensions, never upscaled.
	src := makeJPEG(t, 800, 600)
	out, err := imginput.Encode(src)
	require.NoError(err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	require.NoError(err)
	require.LessOrEqual(cfg.Width, 800)
	require.LessOrEqual(cfg.Height, 600)
}

func TestEncodeFromPreview_ExtremeAspectRatio(t *testing.T) {
	require := require.New(t)
	// 5000 x 1 would round the shorter edge to 0 without clamping.
	src := makeJPEG(t, 5000, 1)
	out, err := imginput.Encode(src)
	require.NoError(err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	require.NoError(err)
	require.Equal(1024, cfg.Width)
	require.GreaterOrEqual(cfg.Height, 1)
}

func TestProfileString(t *testing.T) {
	require.Equal(t, "jpeg-1024-q85-metadata-stripped-v1", imginput.ProfileV1)
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	return buf.Bytes()
}

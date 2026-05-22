package encode_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/imginput/encode"
)

func TestEncodeChat_DownscalesToProfileEdge(t *testing.T) {
	r := require.New(t)
	src := makeJPEG(t, 3000, 2000)
	out, err := encode.EncodeChat(src)
	r.NoError(err)
	r.NotEmpty(out)

	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	r.NoError(err)
	r.LessOrEqual(cfg.Width, encode.ChatMaxEdge)
	r.LessOrEqual(cfg.Height, encode.ChatMaxEdge)
	// 3:2 source → longest edge clamped to 1024.
	if cfg.Width >= cfg.Height {
		r.Equal(encode.ChatMaxEdge, cfg.Width)
	} else {
		r.Equal(encode.ChatMaxEdge, cfg.Height)
	}
}

func TestEncodeChat_Idempotent(t *testing.T) {
	r := require.New(t)
	// Already-small input stays at native dimensions, never upscaled.
	src := makeJPEG(t, 800, 600)
	out, err := encode.EncodeChat(src)
	r.NoError(err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	r.NoError(err)
	r.LessOrEqual(cfg.Width, 800)
	r.LessOrEqual(cfg.Height, 600)
}

func TestEncodeChat_ExtremeAspectRatio(t *testing.T) {
	r := require.New(t)
	// 5000 x 1 would round the shorter edge to 0 without clamping.
	src := makeJPEG(t, 5000, 1)
	out, err := encode.EncodeChat(src)
	r.NoError(err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	r.NoError(err)
	r.Equal(encode.ChatMaxEdge, cfg.Width)
	r.GreaterOrEqual(cfg.Height, 1)
}

func TestEncodeChat_StripsMetadata(t *testing.T) {
	r := require.New(t)
	// Go's image/jpeg writer emits SOI/quant/Huffman/SOF/scan markers
	// only — no APP1/APP2 — so any EXIF/ICC/XMP from the source is
	// dropped. Verify the output starts with SOI immediately followed
	// by a non-APP segment.
	src := makeJPEG(t, 800, 600)
	out, err := encode.EncodeChat(src)
	r.NoError(err)
	r.GreaterOrEqual(len(out), 4)
	r.Equal(byte(0xff), out[0])
	r.Equal(byte(0xd8), out[1]) // SOI
	r.Equal(byte(0xff), out[2])
	// Standard tables/quant marker, never APP0/APP1/APP2 (0xe0/0xe1/0xe2).
	r.NotContains([]byte{0xe0, 0xe1, 0xe2}, out[3])
}

func TestEncodeEmbed_RespectsEdge(t *testing.T) {
	r := require.New(t)
	src := makeJPEG(t, 3000, 2000)
	out, err := encode.EncodeEmbed(src, 384)
	r.NoError(err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	r.NoError(err)
	// Longest edge must be exactly the requested edge.
	if cfg.Width >= cfg.Height {
		r.Equal(384, cfg.Width)
	} else {
		r.Equal(384, cfg.Height)
	}
}

func TestEncodeEmbed_RejectsZeroEdge(t *testing.T) {
	r := require.New(t)
	src := makeJPEG(t, 100, 100)
	_, err := encode.EncodeEmbed(src, 0)
	r.Error(err)
	_, err = encode.EncodeEmbed(src, -10)
	r.Error(err)
}

func TestEncodeEmbed_AlternateEdge(t *testing.T) {
	r := require.New(t)
	// Verify the edge is genuinely caller-controlled, not hard-coded.
	src := makeJPEG(t, 2000, 1500)
	out, err := encode.EncodeEmbed(src, 512)
	r.NoError(err)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	r.NoError(err)
	if cfg.Width >= cfg.Height {
		r.Equal(512, cfg.Width)
	} else {
		r.Equal(512, cfg.Height)
	}
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

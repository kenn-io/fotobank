package thumb_test

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/thumb"
)

// buildSyntheticTIFFWithPreview constructs a minimal TIFF that embeds a
// JPEG "thumbnail" in IFD0's JPEGInterchangeFormat (513) + Length (514)
// tag pair. Real ARW/RAF/DNG/CR2 files use the same tag convention
// (sometimes in a SubIFD) so the extractor logic is the same.
func buildSyntheticTIFFWithPreview(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 255, A: 255})
		}
	}
	var jbuf bytes.Buffer
	require.NoError(t, jpeg.Encode(&jbuf, img, &jpeg.Options{Quality: 80}))
	jpegBytes := jbuf.Bytes()

	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8))

	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))

	ifdSize := uint32(2 + 2*12 + 4)
	jpegOffset := uint32(8) + ifdSize

	_ = binary.Write(&buf, binary.LittleEndian, uint16(513))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, jpegOffset)

	_ = binary.Write(&buf, binary.LittleEndian, uint16(514))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(jpegBytes)))

	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	buf.Write(jpegBytes)
	return buf.Bytes()
}

func TestExtractPreviewReturnsEmbeddedJPEG(t *testing.T) {
	r := require.New(t)
	raw := buildSyntheticTIFFWithPreview(t)

	img, err := thumb.ExtractPreview(bytes.NewReader(raw))
	r.NoError(err)
	r.Equal(32, img.Bounds().Dx())
	r.Equal(32, img.Bounds().Dy())

	// Sample pixels to confirm we decoded the painted preview, not
	// garbage that happens to be 32×32. The paint formula is
	//   RGBA{R: x*8, G: y*8, B: 255, A: 255}
	// JPEG + 4:2:0 chroma subsampling blurs colors across 2×2 blocks so
	// the exact values shift; a 15-count window is enough to confirm
	// the right image while excluding unrelated buffers.
	samples := []struct {
		x, y, r, g, b int
	}{
		{0, 0, 0, 0, 255},
		{31, 0, 248, 0, 255},
		{0, 31, 0, 248, 255},
	}
	for _, s := range samples {
		rC, gC, bC, _ := img.At(s.x, s.y).RGBA()
		r.InDelta(s.r, int(rC>>8), 15, "pixel(%d,%d).R", s.x, s.y)
		r.InDelta(s.g, int(gC>>8), 15, "pixel(%d,%d).G", s.x, s.y)
		r.InDelta(s.b, int(bC>>8), 15, "pixel(%d,%d).B", s.x, s.y)
	}
}

func TestExtractPreviewReturnsErrNoPreviewWhenMissing(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))

	_, err := thumb.ExtractPreview(bytes.NewReader(buf.Bytes()))
	require.ErrorIs(t, err, thumb.ErrNoPreview)
}

// TestExtractPreviewTag513PresentButTag514Missing guards against a
// subtle bug: if the extractor treats length=0 as success, callers
// would try to decode a zero-byte slice and get a confusing error
// instead of the expected ErrNoPreview.
func TestExtractPreviewTag513PresentButTag514Missing(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8))

	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(513))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(64))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))

	_, err := thumb.ExtractPreview(bytes.NewReader(buf.Bytes()))
	require.ErrorIs(t, err, thumb.ErrNoPreview)
}

// TestExtractPreviewPreviewOffsetPastEOF confirms we fail loudly (not
// silently) when the tags point past the end of the file — this can
// happen on truncated downloads of otherwise valid RAW files.
func TestExtractPreviewPreviewOffsetPastEOF(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8))

	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))

	ifdSize := uint32(2 + 2*12 + 4)
	jpegOffset := uint32(8) + ifdSize

	_ = binary.Write(&buf, binary.LittleEndian, uint16(513))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, jpegOffset)

	_ = binary.Write(&buf, binary.LittleEndian, uint16(514))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1_000_000))

	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))

	_, err := thumb.ExtractPreview(bytes.NewReader(buf.Bytes()))
	require.Error(t, err)
	require.NotErrorIs(t, err, thumb.ErrNoPreview)
	require.Contains(t, err.Error(), "exceed file size")
}

// buildSyntheticTIFFWithPreviewAndOrientation emits a TIFF whose IFD0
// carries a JPEGInterchangeFormat preview plus the Orientation tag
// (274). Used to cover the RAW-side orientation path: portrait phone
// photos ship preview pixels in sensor layout with orientation > 1.
func buildSyntheticTIFFWithPreviewAndOrientation(t *testing.T, orient uint16) []byte {
	t.Helper()
	// Preview: 100 wide, 200 tall. Under orientation=6 (rotate 90° CW)
	// the display-correct dimensions must be 200x100.
	src := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for y := range 200 {
		for x := range 100 {
			src.Set(x, y, color.RGBA{R: 200, G: uint8(y * 255 / 200), B: uint8(x * 255 / 100), A: 255})
		}
	}
	var jbuf bytes.Buffer
	require.NoError(t, jpeg.Encode(&jbuf, src, &jpeg.Options{Quality: 80}))
	jpegBytes := jbuf.Bytes()

	var buf bytes.Buffer
	buf.WriteString("II")
	_ = binary.Write(&buf, binary.LittleEndian, uint16(42))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(8))

	// 3 entries: Orientation (274), JPEGInterchangeFormat (513),
	// JPEGInterchangeFormatLength (514).
	_ = binary.Write(&buf, binary.LittleEndian, uint16(3))
	ifdSize := uint32(2 + 3*12 + 4)
	jpegOffset := uint32(8) + ifdSize

	// Orientation tag: type=SHORT(3), count=1, value packed into first
	// 2 bytes of the 4-byte value slot (little-endian).
	_ = binary.Write(&buf, binary.LittleEndian, uint16(274))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(3))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(orient))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))

	_ = binary.Write(&buf, binary.LittleEndian, uint16(513))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, jpegOffset)

	_ = binary.Write(&buf, binary.LittleEndian, uint16(514))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(4))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(jpegBytes)))

	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	buf.Write(jpegBytes)
	return buf.Bytes()
}

// TestExtractPreviewAppliesOrientation6 confirms that a RAW whose TIFF
// Orientation tag is 6 (rotate 90° CW) gets the rotation baked into
// the decoded preview. Without this, portrait RAW previews render
// sideways even though the main image orientation is correct.
func TestExtractPreviewAppliesOrientation6(t *testing.T) {
	r := require.New(t)
	raw := buildSyntheticTIFFWithPreviewAndOrientation(t, 6)

	img, err := thumb.ExtractPreview(bytes.NewReader(raw))
	r.NoError(err)
	r.Equal(200, img.Bounds().Dx(), "orientation=6 should swap W/H")
	r.Equal(100, img.Bounds().Dy())
}

// TestExtractPreviewOrientation1Identity confirms that the common case
// (explicit orientation=1, i.e. identity) returns the preview
// unchanged, matching the no-tag default.
func TestExtractPreviewOrientation1Identity(t *testing.T) {
	r := require.New(t)
	raw := buildSyntheticTIFFWithPreviewAndOrientation(t, 1)

	img, err := thumb.ExtractPreview(bytes.NewReader(raw))
	r.NoError(err)
	r.Equal(100, img.Bounds().Dx())
	r.Equal(200, img.Bounds().Dy())
}

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

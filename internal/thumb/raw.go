package thumb

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"

	"github.com/dsoprea/go-exif/v3"
)

// ExtractPreview walks the TIFF/EXIF IFDs in src and returns the
// embedded JPEG preview decoded as an image.Image. Supports the
// JPEGInterchangeFormat (513) + JPEGInterchangeFormatLength (514)
// convention used by ARW, RAF, DNG, and CR2 — the preview may live
// in IFD0 or a SubIFD depending on the manufacturer.
//
// Returns ErrNoPreview when no JPEG preview is present; callers
// translate this to thumb_status='no_preview' without retrying.
func ExtractPreview(src io.Reader) (image.Image, error) {
	bs, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("read raw: %w", err)
	}
	offset, length, found, err := findPreviewTags(bs)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: no JPEGInterchangeFormat tag in IFDs", ErrNoPreview)
	}
	if uint64(offset)+uint64(length) > uint64(len(bs)) {
		return nil, fmt.Errorf(
			"thumb: preview bytes (offset=%d len=%d) exceed file size %d",
			offset, length, len(bs),
		)
	}
	img, err := jpeg.Decode(bytes.NewReader(bs[offset : offset+length]))
	if err != nil {
		return nil, fmt.Errorf("decode embedded jpeg: %w", err)
	}
	return img, nil
}

// findPreviewTags scans the flat list of EXIF entries (which the go-exif
// walker produces by descending into every reachable IFD) for the
// (JPEGInterchangeFormat, JPEGInterchangeFormatLength) pair that
// points to an embedded JPEG. Returns (offset, length, found, err).
// A parse error is treated as "no preview" so callers see ErrNoPreview
// rather than a confusing lower-level message.
func findPreviewTags(bs []byte) (offset uint32, length uint32, found bool, err error) {
	entries, _, err := exif.GetFlatExifDataUniversalSearch(bs, nil, false)
	if err != nil {
		return 0, 0, false, nil
	}
	for _, e := range entries {
		switch e.TagId {
		case 513:
			if v, ok := firstU32(e.Value); ok {
				offset = v
			}
		case 514:
			if v, ok := firstU32(e.Value); ok {
				length = v
			}
		}
	}
	if offset > 0 && length > 0 {
		return offset, length, true, nil
	}
	return 0, 0, false, nil
}

// firstU32 returns the first uint32 in a slice-typed EXIF value, or
// (0, false) when the value is not a non-empty []uint32.
func firstU32(v any) (uint32, bool) {
	if vs, ok := v.([]uint32); ok && len(vs) > 0 {
		return vs[0], true
	}
	return 0, false
}

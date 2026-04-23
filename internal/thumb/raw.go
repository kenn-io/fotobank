package thumb

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"

	"github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
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
	if int(offset)+int(length) > len(bs) {
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

// findPreviewTags scans every IFD (IFD0 and any SubIFDs) for the
// (JPEGInterchangeFormat, JPEGInterchangeFormatLength) pair that
// points to an embedded JPEG. Returns (offset, length, found, err).
func findPreviewTags(bs []byte) (offset uint32, length uint32, found bool, err error) {
	im, err := exifcommon.NewIfdMappingWithStandard()
	if err != nil {
		return 0, 0, false, fmt.Errorf("ifd mapping: %w", err)
	}
	ti := exif.NewTagIndex()

	entries, _, err := exif.GetFlatExifDataUniversalSearch(bs, nil, false)
	if err != nil {
		return findPreviewTagsTIFF(bs, im, ti)
	}
	for _, e := range entries {
		switch e.TagId {
		case 513:
			if vs, ok := e.Value.([]uint32); ok && len(vs) > 0 {
				offset = vs[0]
			}
		case 514:
			if vs, ok := e.Value.([]uint32); ok && len(vs) > 0 {
				length = vs[0]
			}
		}
	}
	if offset > 0 && length > 0 {
		return offset, length, true, nil
	}
	return findPreviewTagsTIFF(bs, im, ti)
}

func findPreviewTagsTIFF(
	bs []byte,
	im *exifcommon.IfdMapping,
	ti *exif.TagIndex,
) (offset uint32, length uint32, found bool, err error) {
	_, index, err := exif.Collect(im, ti, bs)
	if err != nil {
		return 0, 0, false, nil
	}
	for _, ifd := range index.Ifds {
		for _, ite := range ifd.Entries() {
			if ite.TagId() == 513 {
				if vs, err := ite.Value(); err == nil {
					if u32s, ok := vs.([]uint32); ok && len(u32s) > 0 {
						offset = u32s[0]
					}
				}
			}
			if ite.TagId() == 514 {
				if vs, err := ite.Value(); err == nil {
					if u32s, ok := vs.([]uint32); ok && len(u32s) > 0 {
						length = u32s[0]
					}
				}
			}
		}
	}
	if offset > 0 && length > 0 {
		return offset, length, true, nil
	}
	return 0, 0, false, nil
}

package thumb

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/dsoprea/go-exif/v3"
)

// ErrNoPreview indicates the source format is one we intentionally do
// not decode (HEIC in particular). Callers translate this
// to thumb_status='no_preview' without retrying.
var ErrNoPreview = errors.New("thumb: no preview available")

// Decode returns an image.Image from src, applying EXIF orientation so
// downstream resize/encode produces display-correct pixels. Dispatch
// is by MIME type. PNG has no EXIF orientation by spec so we decode
// it as-is. HEIC requires a decoder that is not currently included.
// (returns ErrNoPreview here).
func Decode(mime string, src io.Reader) (image.Image, error) {
	switch mime {
	case "image/jpeg":
		return decodeJPEG(src)
	case "image/gif":
		g, err := gif.Decode(src)
		if err != nil {
			return nil, fmt.Errorf("gif decode: %w", err)
		}
		return g, nil
	case "image/png":
		p, err := png.Decode(src)
		if err != nil {
			return nil, fmt.Errorf("png decode: %w", err)
		}
		return p, nil
	case "image/heic", "image/heif":
		return nil, fmt.Errorf("%w: %s (HEIC decoding requires CGO; deferred)", ErrNoPreview, mime)
	}
	return nil, fmt.Errorf("thumb: unsupported mime %q", mime)
}

func decodeJPEG(src io.Reader) (image.Image, error) {
	bs, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("read jpeg: %w", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(bs))
	if err != nil {
		return nil, fmt.Errorf("jpeg decode: %w", err)
	}
	return applyOrientation(img, readOrientation(bs)), nil
}

// readOrientation returns the EXIF Orientation tag (1-8) for the given
// JPEG bytes, or 1 (identity) if no EXIF segment is present, the tag
// is missing, or parsing fails. Per the EXIF spec, values outside 1-8
// are invalid and treated as identity.
func readOrientation(bs []byte) int {
	raw, err := exif.SearchAndExtractExif(bs)
	if err != nil {
		return 1
	}
	entries, _, err := exif.GetFlatExifData(raw, nil)
	if err != nil {
		return 1
	}
	for _, e := range entries {
		if e.TagName != "Orientation" {
			continue
		}
		if v, ok := e.Value.([]uint16); ok && len(v) > 0 {
			n := int(v[0])
			if n >= 1 && n <= 8 {
				return n
			}
		}
	}
	return 1
}

// applyOrientation remaps pixels to bake the EXIF orientation into the
// image buffer. Orientation values are defined at
// https://exiftool.org/TagNames/EXIF.html#Orientation: 1=identity,
// 2=hflip, 3=rot180, 4=vflip, 5=transpose, 6=rot90CW, 7=transverse,
// 8=rot90CCW. 5-8 swap width and height.
func applyOrientation(img image.Image, orient int) image.Image {
	if orient == 1 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var outW, outH int
	var remap func(x, y int) (int, int)
	switch orient {
	case 2:
		outW, outH = w, h
		remap = func(x, y int) (int, int) { return w - 1 - x, y }
	case 3:
		outW, outH = w, h
		remap = func(x, y int) (int, int) { return w - 1 - x, h - 1 - y }
	case 4:
		outW, outH = w, h
		remap = func(x, y int) (int, int) { return x, h - 1 - y }
	case 5:
		outW, outH = h, w
		remap = func(x, y int) (int, int) { return y, x }
	case 6:
		outW, outH = h, w
		remap = func(x, y int) (int, int) { return y, h - 1 - x }
	case 7:
		outW, outH = h, w
		remap = func(x, y int) (int, int) { return w - 1 - y, h - 1 - x }
	case 8:
		outW, outH = h, w
		remap = func(x, y int) (int, int) { return w - 1 - y, x }
	default:
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for y := range outH {
		for x := range outW {
			sx, sy := remap(x, y)
			dst.Set(x, y, img.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

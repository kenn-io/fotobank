// Package imginput re-encodes existing preview-tier JPEGs into the
// fixed AI input profile: max edge 1024 px, JPEG q=85, EXIF/ICC stripped.
// We never look at original files; the preview is already a sanitized
// downscale produced by the thumb pipeline.
package imginput

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	"golang.org/x/image/draw"
)

// ProfileV1 is the canonical name persisted on every ai_results row.
// Bumping requires a new constant and re-running the gap scanner.
const ProfileV1 = "jpeg-1024-q85-metadata-stripped-v1"

// MaxEdge and Quality match ProfileV1.
const (
	MaxEdge = 1024
	Quality = 85
)

// Encode takes preview-tier JPEG bytes (e.g. the 2560-edge thumb tier)
// and returns the AI-input JPEG. The encoder writes only the JPEG
// scan + Huffman/quant tables — no APP markers — so EXIF, ICC, and
// XMP from the source are dropped. Vectors are always JPEG; callers
// pass JPEG bytes from the existing preview store.
func Encode(srcJPEG []byte) ([]byte, error) {
	src, err := jpeg.Decode(bytes.NewReader(srcJPEG))
	if err != nil {
		return nil, fmt.Errorf("decode preview jpeg: %w", err)
	}
	dst := scaleToProfile(src)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: Quality}); err != nil {
		return nil, fmt.Errorf("encode profile jpeg: %w", err)
	}
	return out.Bytes(), nil
}

// scaleToProfile downscales src so its longest edge is at most MaxEdge.
// Already-small images are returned untouched (no upscale).
func scaleToProfile(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= MaxEdge && h <= MaxEdge {
		return src
	}
	var nw, nh int
	if w >= h {
		nw = MaxEdge
		nh = int(float64(h) * float64(MaxEdge) / float64(w))
	} else {
		nh = MaxEdge
		nw = int(float64(w) * float64(MaxEdge) / float64(h))
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

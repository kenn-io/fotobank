// Package encode re-encodes preview-tier JPEGs into the AI input
// profiles consumed by chat and embed tasks. The encoders never look
// at original files; the preview is already a sanitized downscale
// produced by the thumb pipeline.
//
// The encoder writes only the JPEG scan + Huffman/quant tables — no
// APP markers — so EXIF, ICC, and XMP from the source are dropped.
// Vectors are always JPEG; callers pass JPEG bytes from the existing
// preview store.
package encode

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	"golang.org/x/image/draw"
)

// ChatMaxEdge and ChatQuality match the chat-task input profile name
// imginput.ProfileV1: "jpeg-1024-q85-metadata-stripped-v1".
const (
	ChatMaxEdge = 1024
	ChatQuality = 85
)

// EncodeChat re-encodes srcJPEG to the chat-task input profile:
// 1024-edge resize, q85 JPEG, metadata stripped.
func EncodeChat(srcJPEG []byte) ([]byte, error) {
	return encodeJPEG(srcJPEG, ChatMaxEdge, ChatQuality)
}

// EncodeEmbed re-encodes srcJPEG to the embed-task input profile:
// caller-specified longest edge, q85 JPEG, metadata stripped. The
// embed model selects its own preferred edge at boot via the modality
// probe, so this entry point is parameterized.
func EncodeEmbed(srcJPEG []byte, edge int) ([]byte, error) {
	if edge <= 0 {
		return nil, fmt.Errorf("encode embed: edge must be > 0")
	}
	return encodeJPEG(srcJPEG, edge, ChatQuality)
}

// encodeJPEG decodes src, downscales so the longest edge is at most
// maxEdge, and re-encodes as JPEG at the given quality. Already-small
// images are returned at native dimensions (no upscale). Metadata is
// stripped because Go's image/jpeg writer emits no APP markers.
func encodeJPEG(srcJPEG []byte, maxEdge, quality int) ([]byte, error) {
	src, err := jpeg.Decode(bytes.NewReader(srcJPEG))
	if err != nil {
		return nil, fmt.Errorf("decode preview jpeg: %w", err)
	}
	dst := scaleTo(src, maxEdge)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("encode profile jpeg: %w", err)
	}
	return out.Bytes(), nil
}

// scaleTo downscales src so its longest edge is at most maxEdge.
// Already-small images are returned untouched (no upscale).
func scaleTo(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return src
	}
	var nw, nh int
	if w >= h {
		nw = maxEdge
		nh = int(float64(h) * float64(maxEdge) / float64(w))
	} else {
		nh = maxEdge
		nw = int(float64(w) * float64(maxEdge) / float64(h))
	}
	// Extreme aspect ratios can round the shorter edge to 0; clamp so
	// the destination image stays valid.
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

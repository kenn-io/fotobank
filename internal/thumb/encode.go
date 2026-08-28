package thumb

import (
	"fmt"
	"image"
	"image/jpeg"
	"io"

	"golang.org/x/image/draw"
)

// Resize returns a new image whose longest edge is at most maxEdge
// pixels, preserving aspect ratio. Images already within the budget
// pass through unchanged — upscaling produces blurry thumbnails and
// wastes bytes. The kernel is draw.CatmullRom (bicubic), which is the
// Go team's default good-quality downscaler.
// Returns the source unchanged when it's already within budget (or
// when maxEdge <= 0); otherwise returns a fresh *image.RGBA.
func Resize(src image.Image, maxEdge int) image.Image {
	if maxEdge <= 0 {
		return src
	}
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= maxEdge && srcH <= maxEdge {
		return src
	}
	var dstW, dstH int
	if srcW >= srcH {
		dstW = maxEdge
		dstH = srcH * maxEdge / srcW
	} else {
		dstH = maxEdge
		dstW = srcW * maxEdge / srcH
	}
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// EncodeJPEG writes img to w as JPEG at the given quality (1-100).
// JPEG is used instead of WebP because the available pure-Go WebP encoder
// is lossless-only.
func EncodeJPEG(w io.Writer, img image.Image, quality int) error {
	if quality < 1 || quality > 100 {
		return fmt.Errorf("thumb: jpeg quality out of range: %d", quality)
	}
	return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
}

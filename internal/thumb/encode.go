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
func Resize(src image.Image, maxEdge int) image.Image {
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
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// EncodeJPEG writes img to w as JPEG at the given quality (1-100).
// Plan C chose JPEG over WebP because the only pure-Go WebP encoder
// is lossless-only; see spec §7.2.
func EncodeJPEG(w io.Writer, img image.Image, quality int) error {
	if quality < 1 || quality > 100 {
		return fmt.Errorf("thumb: jpeg quality out of range: %d", quality)
	}
	return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
}

package thumb

import (
	"errors"
	"image"
)

// ErrNoPreview indicates the source format is one we intentionally do
// not decode (HEIC in particular). Callers translate this
// to thumb_status='no_preview' without retrying.
var ErrNoPreview = errors.New("thumb: no preview available")

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

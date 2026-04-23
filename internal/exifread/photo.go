package exifread

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
)

// ExtractPhoto reads EXIF from the given path and returns the
// normalised metadata. Files without an EXIF segment return an
// empty Metadata with a nil error; only read/parse errors surface.
func ExtractPhoto(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()

	raw, err := exif.SearchAndExtractExifWithReader(f)
	if err != nil {
		if errors.Is(err, exif.ErrNoExif) {
			return Metadata{}, nil
		}
		return Metadata{}, fmt.Errorf("search exif: %w", err)
	}
	return parseExif(raw)
}

func parseExif(raw []byte) (Metadata, error) {
	entries, _, err := exif.GetFlatExifData(raw, nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("parse exif: %w", err)
	}
	m := Metadata{}
	by := make(map[string]exif.ExifTag, len(entries))
	for _, e := range entries {
		by[e.TagName] = e
	}
	if ts, ok := parseExifTimestamp(by); ok {
		m.Timestamp = &ts
	}
	if v := stringTag(by, "Make"); v != "" {
		m.Make = strings.TrimSpace(v)
	}
	if v := stringTag(by, "Model"); v != "" {
		m.Model = strings.TrimSpace(v)
	}
	if v := stringTag(by, "FocalLength"); v != "" {
		m.FocalLength = v
	}
	if v := stringTag(by, "ShutterSpeedValue"); v != "" {
		m.ShutterSpeed = v
	} else if v := stringTag(by, "ExposureTime"); v != "" {
		m.ShutterSpeed = v
	}
	if v := rationalTag(by, "FNumber"); v > 0 {
		m.Aperture = v
	}
	if iso := intTag(by, "ISOSpeedRatings"); iso > 0 {
		m.ISO = iso
	}
	if w := intTag(by, "ExifImageWidth"); w > 0 {
		m.Width = w
	}
	if h := intTag(by, "ExifImageLength"); h > 0 {
		m.Height = h
	}
	if _, ok := by["PreviewImageStart"]; ok {
		m.HasEmbeddedPreview = true
	} else if _, ok := by["ThumbnailImageStart"]; ok {
		m.HasEmbeddedPreview = true
	}
	return m, nil
}

func parseExifTimestamp(by map[string]exif.ExifTag) (time.Time, bool) {
	candidates := []string{"DateTimeOriginal", "DateTimeDigitized", "DateTime"}
	for _, name := range candidates {
		if v := stringTag(by, name); v != "" {
			// EXIF format: "2024:06:15 14:30:22"
			if t, err := time.ParseInLocation("2006:01:02 15:04:05", v, time.UTC); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

func stringTag(by map[string]exif.ExifTag, name string) string {
	e, ok := by[name]
	if !ok {
		return ""
	}
	if s, ok := e.Value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", e.Value)
}

func intTag(by map[string]exif.ExifTag, name string) int {
	e, ok := by[name]
	if !ok {
		return 0
	}
	switch v := e.Value.(type) {
	case []uint16:
		if len(v) > 0 {
			return int(v[0])
		}
	case uint16:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	s := fmt.Sprintf("%v", e.Value)
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return 0
}

func rationalTag(by map[string]exif.ExifTag, name string) float64 {
	e, ok := by[name]
	if !ok {
		return 0
	}
	switch v := e.Value.(type) {
	case []exifcommon.Rational:
		if len(v) > 0 && v[0].Denominator != 0 {
			return float64(v[0].Numerator) / float64(v[0].Denominator)
		}
	case exifcommon.Rational:
		if v.Denominator != 0 {
			return float64(v.Numerator) / float64(v.Denominator)
		}
	}
	return 0
}

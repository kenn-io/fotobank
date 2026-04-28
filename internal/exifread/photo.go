package exifread

import (
	"errors"
	"fmt"
	"io"
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
	return ExtractPhotoFromReader(f)
}

// ExtractPhotoFromReader is identical to ExtractPhoto but reads from r
// instead of a file path. Used by the gps backfill CLI to stream EXIF
// from storage.Store.ReadRange without an intermediate temp file.
func ExtractPhotoFromReader(r io.Reader) (Metadata, error) {
	raw, err := exif.SearchAndExtractExifWithReader(r)
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
	if lat, lon, ok := parseExifGPSCoords(by); ok {
		m.Latitude = &lat
		m.Longitude = &lon
	}
	if t, ok := parseExifGPSTimestamp(by); ok {
		m.GPSAt = &t
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

// parseExifGPSCoords enforces the validation matrix from F2.1 spec §5.3.
// Returns (lat, lon, true) only when every condition holds: rationals
// present and non-zero-denominator, refs are exactly N|S and E|W,
// resulting decimals are in -90..90 / -180..180, and (lat, lon) is not
// the literal null-island origin.
func parseExifGPSCoords(by map[string]exif.ExifTag) (float64, float64, bool) {
	latRaw, ok1 := by["GPSLatitude"]
	lonRaw, ok2 := by["GPSLongitude"]
	latRefRaw, ok3 := by["GPSLatitudeRef"]
	lonRefRaw, ok4 := by["GPSLongitudeRef"]
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return 0, 0, false
	}
	latDMS, ok := dmsRationals(latRaw)
	if !ok {
		return 0, 0, false
	}
	lonDMS, ok := dmsRationals(lonRaw)
	if !ok {
		return 0, 0, false
	}
	latRef, ok := strictRef(latRefRaw, "N", "S")
	if !ok {
		return 0, 0, false
	}
	lonRef, ok := strictRef(lonRefRaw, "E", "W")
	if !ok {
		return 0, 0, false
	}
	lat := dmsToDecimal(latDMS, latRef)
	lon := dmsToDecimal(lonDMS, lonRef)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, false
	}
	if lat == 0 && lon == 0 {
		// Null-island anti-pattern: cameras often emit literal 0,0
		// before GPS has acquired a fix. We prefer to lose any real
		// 0,0 photo over ingesting noise on every broken-fix camera.
		return 0, 0, false
	}
	return lat, lon, true
}

// dmsRationals extracts a degrees/minutes/seconds triple. Returns ok=false
// if the value is not exactly three rationals, if any denominator is
// zero, or if minutes or seconds fall outside [0, 60). The min/sec bound
// catches malformed inputs like 88°120'0" that would otherwise silently
// normalise to a different valid-looking coordinate.
func dmsRationals(t exif.ExifTag) ([3]exifcommon.Rational, bool) {
	rs, ok := t.Value.([]exifcommon.Rational)
	if !ok || len(rs) != 3 {
		return [3]exifcommon.Rational{}, false
	}
	for i := range 3 {
		if rs[i].Denominator == 0 {
			return [3]exifcommon.Rational{}, false
		}
	}
	min := float64(rs[1].Numerator) / float64(rs[1].Denominator)
	sec := float64(rs[2].Numerator) / float64(rs[2].Denominator)
	if min < 0 || min >= 60 || sec < 0 || sec >= 60 {
		return [3]exifcommon.Rational{}, false
	}
	return [3]exifcommon.Rational{rs[0], rs[1], rs[2]}, true
}

// strictRef returns the value if it is exactly one of the two allowed
// strings (length-1, case-sensitive); otherwise ok=false.
func strictRef(t exif.ExifTag, a, b string) (string, bool) {
	s, ok := t.Value.(string)
	if !ok {
		return "", false
	}
	if s == a || s == b {
		return s, true
	}
	return "", false
}

// dmsToDecimal converts a DMS rational triple + cardinal ref into a
// signed decimal degree. Caller has already validated denominators
// are non-zero (see dmsRationals).
func dmsToDecimal(dms [3]exifcommon.Rational, ref string) float64 {
	deg := float64(dms[0].Numerator) / float64(dms[0].Denominator)
	min := float64(dms[1].Numerator) / float64(dms[1].Denominator)
	sec := float64(dms[2].Numerator) / float64(dms[2].Denominator)
	v := deg + min/60.0 + sec/3600.0
	if ref == "S" || ref == "W" {
		v = -v
	}
	return v
}

// parseExifGPSTimestamp combines GPSDateStamp ("YYYY:MM:DD") and
// GPSTimeStamp (rational triple, UTC) into a single time.Time. Returns
// (zero, false) on any validation failure: missing tags, zero
// denominators, out-of-range hour/minute/second, or fractional
// hour/minute (only fractional seconds are preserved as nanoseconds —
// the EXIF spec specifies minutes and hours as whole numbers).
func parseExifGPSTimestamp(by map[string]exif.ExifTag) (time.Time, bool) {
	dateRaw, ok := by["GPSDateStamp"]
	if !ok {
		return time.Time{}, false
	}
	dateStr, ok := dateRaw.Value.(string)
	if !ok {
		return time.Time{}, false
	}
	d, err := time.ParseInLocation("2006:01:02", dateStr, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	timeRaw, ok := by["GPSTimeStamp"]
	if !ok {
		return time.Time{}, false
	}
	hms, ok := dmsRationals(timeRaw)
	if !ok {
		return time.Time{}, false
	}
	// Hour must be whole and in [0, 24). dmsRationals already bounds
	// minute/second to [0, 60).
	if hms[0].Numerator%hms[0].Denominator != 0 {
		return time.Time{}, false
	}
	hour := hms[0].Numerator / hms[0].Denominator
	if hour >= 24 {
		return time.Time{}, false
	}
	if hms[1].Numerator%hms[1].Denominator != 0 {
		return time.Time{}, false
	}
	minute := hms[1].Numerator / hms[1].Denominator
	// Seconds may be fractional (modern phones emit sub-second
	// precision); preserve as nanoseconds.
	secNum := uint64(hms[2].Numerator)
	secDen := uint64(hms[2].Denominator)
	whole := secNum / secDen
	frac := secNum - whole*secDen
	nsec := int(frac * 1_000_000_000 / secDen)
	return time.Date(d.Year(), d.Month(), d.Day(),
		int(hour), int(minute), int(whole), nsec, time.UTC), true
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

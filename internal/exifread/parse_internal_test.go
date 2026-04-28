package exifread

import (
	"testing"
	"time"

	exif "github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
	"github.com/stretchr/testify/require"
)

// dms3 makes a DMS triple for use as GPSLatitude / GPSLongitude /
// GPSTimeStamp.
func dms3(d, m, s int64) exif.ExifTag {
	return exif.ExifTag{Value: []exifcommon.Rational{
		{Numerator: uint32(d), Denominator: 1},
		{Numerator: uint32(m), Denominator: 1},
		{Numerator: uint32(s), Denominator: 1},
	}}
}

func dms3Denom(d, dDen, m, mDen, s, sDen int64) exif.ExifTag {
	return exif.ExifTag{Value: []exifcommon.Rational{
		{Numerator: uint32(d), Denominator: uint32(dDen)},
		{Numerator: uint32(m), Denominator: uint32(mDen)},
		{Numerator: uint32(s), Denominator: uint32(sDen)},
	}}
}

func strTag(s string) exif.ExifTag { return exif.ExifTag{Value: s} }

func TestParseExifGPSCoordsValidation(t *testing.T) {
	type tc struct {
		name string
		by   map[string]exif.ExifTag
		ok   bool
		lat  float64
		lon  float64
	}
	clean := map[string]exif.ExifTag{
		"GPSLatitude":     dms3(48, 51, 24),
		"GPSLatitudeRef":  strTag("N"),
		"GPSLongitude":    dms3(2, 21, 8),
		"GPSLongitudeRef": strTag("E"),
	}
	cases := []tc{
		{"clean fix Paris-ish", clean, true, 48.856_667, 2.352_222},
		{"missing latitude", map[string]exif.ExifTag{
			"GPSLatitudeRef": strTag("N"), "GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"missing longitude ref", map[string]exif.ExifTag{
			"GPSLatitude": dms3(48, 51, 24), "GPSLatitudeRef": strTag("N"), "GPSLongitude": dms3(2, 21, 8),
		}, false, 0, 0},
		{"latitude zero denominator", map[string]exif.ExifTag{
			"GPSLatitude": dms3Denom(48, 0, 51, 1, 24, 1), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"latitude ref garbage", map[string]exif.ExifTag{
			"GPSLatitude": dms3(48, 51, 24), "GPSLatitudeRef": strTag("Q"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"latitude ref lowercase", map[string]exif.ExifTag{
			"GPSLatitude": dms3(48, 51, 24), "GPSLatitudeRef": strTag("n"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"latitude out of range", map[string]exif.ExifTag{
			"GPSLatitude": dms3(91, 0, 0), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"longitude out of range", map[string]exif.ExifTag{
			"GPSLatitude": dms3(48, 51, 24), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(181, 0, 0), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		{"null island", map[string]exif.ExifTag{
			"GPSLatitude": dms3(0, 0, 0), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(0, 0, 0), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := require.New(t)
			lat, lon, ok := parseExifGPSCoords(c.by)
			r.Equal(c.ok, ok)
			if ok {
				r.InDelta(c.lat, lat, 1e-3)
				r.InDelta(c.lon, lon, 1e-3)
			}
		})
	}
}

func TestParseExifGPSTimestampValidation(t *testing.T) {
	type tc struct {
		name string
		by   map[string]exif.ExifTag
		ok   bool
	}
	clean := map[string]exif.ExifTag{
		"GPSDateStamp": strTag("2024:06:15"),
		"GPSTimeStamp": dms3(14, 30, 22),
	}
	cases := []tc{
		{"clean", clean, true},
		{"missing date", map[string]exif.ExifTag{"GPSTimeStamp": dms3(14, 30, 22)}, false},
		{"missing time", map[string]exif.ExifTag{"GPSDateStamp": strTag("2024:06:15")}, false},
		{"date parse fails", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("not-a-date"), "GPSTimeStamp": dms3(14, 30, 22),
		}, false},
		{"hour out of range", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"), "GPSTimeStamp": dms3(25, 0, 0),
		}, false},
		{"minute out of range", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"), "GPSTimeStamp": dms3(14, 60, 0),
		}, false},
		{"second out of range", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"), "GPSTimeStamp": dms3(14, 30, 60),
		}, false},
		{"hour zero denominator", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"),
			"GPSTimeStamp": dms3Denom(14, 0, 30, 1, 22, 1),
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := require.New(t)
			_, ok := parseExifGPSTimestamp(c.by)
			r.Equal(c.ok, ok)
		})
	}
}

func TestParseExifGPSCleanTimestampInUTC(t *testing.T) {
	r := require.New(t)
	by := map[string]exif.ExifTag{
		"GPSDateStamp": strTag("2024:06:15"),
		"GPSTimeStamp": dms3(14, 30, 22),
	}
	got, ok := parseExifGPSTimestamp(by)
	r.True(ok)
	r.Equal(time.UTC, got.Location())
	r.Equal(2024, got.Year())
	r.Equal(time.June, got.Month())
	r.Equal(15, got.Day())
	r.Equal(14, got.Hour())
	r.Equal(30, got.Minute())
	r.Equal(22, got.Second())
}

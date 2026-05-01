package exifread

import (
	"encoding/binary"
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
		// S/W hemisphere refs must produce negative decimals — locks
		// the sign-flip in dmsToDecimal against an accidental drop.
		{"south west fix", map[string]exif.ExifTag{
			"GPSLatitude": dms3(33, 52, 8), "GPSLatitudeRef": strTag("S"),
			"GPSLongitude": dms3(151, 12, 34), "GPSLongitudeRef": strTag("W"),
		}, true, -33.868_889, -151.209_444},
		// Real cameras commonly emit non-1 denominators (e.g. seconds
		// as 240/10). Verify the math handles them on the happy path,
		// not just the failure (zero-denominator) path.
		{"clean fix non-unit denoms", map[string]exif.ExifTag{
			"GPSLatitude":     dms3Denom(48, 1, 51, 1, 240, 10),
			"GPSLatitudeRef":  strTag("N"),
			"GPSLongitude":    dms3Denom(2, 1, 21, 1, 80, 10),
			"GPSLongitudeRef": strTag("E"),
		}, true, 48.856_667, 2.352_222},
		// Minutes >= 60 must be rejected. 88°120'0" would silently
		// normalise to 90° otherwise; reject as malformed.
		{"latitude minutes out of range", map[string]exif.ExifTag{
			"GPSLatitude": dms3(88, 120, 0), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		// Seconds >= 60 must be rejected for the same reason.
		{"latitude seconds out of range", map[string]exif.ExifTag{
			"GPSLatitude": dms3(48, 51, 90), "GPSLatitudeRef": strTag("N"),
			"GPSLongitude": dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
		}, false, 0, 0},
		// Exactly-three rationals required: a 4-element value (some
		// cameras pad with bearing or altitude) must be rejected, not
		// silently truncated.
		{"latitude four rationals", map[string]exif.ExifTag{
			"GPSLatitude": exif.ExifTag{Value: []exifcommon.Rational{
				{Numerator: 48, Denominator: 1},
				{Numerator: 51, Denominator: 1},
				{Numerator: 24, Denominator: 1},
				{Numerator: 0, Denominator: 1},
			}},
			"GPSLatitudeRef": strTag("N"),
			"GPSLongitude":   dms3(2, 21, 8), "GPSLongitudeRef": strTag("E"),
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
		// Non-1 denominators on the happy path (e.g. 220/10 for the
		// seconds field) — the same math path as the coords matrix.
		{"clean non-unit denoms", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"),
			"GPSTimeStamp": dms3Denom(14, 1, 30, 1, 220, 10),
		}, true},
		// Fractional hour must be rejected: 14.5 hours would be
		// truncated to 14:00 silently otherwise.
		{"fractional hour rejected", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"),
			"GPSTimeStamp": dms3Denom(29, 2, 30, 1, 22, 1),
		}, false},
		// Fractional minute must be rejected for the same reason.
		{"fractional minute rejected", map[string]exif.ExifTag{
			"GPSDateStamp": strTag("2024:06:15"),
			"GPSTimeStamp": dms3Denom(14, 1, 61, 2, 22, 1),
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

// TestParseExifGPSTimestampPreservesFractionalSeconds locks in the
// fractional-second preservation: a GPSTimeStamp seconds rational of
// 45/2 must yield 22 whole seconds and 500_000_000 nanoseconds. A
// regression that truncated back to whole seconds (zero ns) would
// slip through TestParseExifGPSTimestampValidation, which only
// asserts ok.
func TestParseExifGPSTimestampPreservesFractionalSeconds(t *testing.T) {
	r := require.New(t)
	by := map[string]exif.ExifTag{
		"GPSDateStamp": strTag("2024:06:15"),
		// 14:30:22.5 — second numerator/denominator = 45/2.
		"GPSTimeStamp": dms3Denom(14, 1, 30, 1, 45, 2),
	}
	got, ok := parseExifGPSTimestamp(by)
	r.True(ok)
	r.Equal(22, got.Second(), "whole seconds")
	r.Equal(500_000_000, got.Nanosecond(), "fractional seconds preserved as ns")
}

// buildExifWithLensModel returns a minimal serialized EXIF segment
// containing Make, Model, and a LensModel sub-IFD tag (0xA434). Used
// by the LensModel parsing test to avoid dragging a binary fixture
// through a check-in. The test fixtures under testdata/exif don't have
// LensModel set, so this is the cleanest path to exercise the tag.
func buildExifWithLensModel(t *testing.T, lens string) []byte {
	r := require.New(t)
	im, err := exifcommon.NewIfdMappingWithStandard()
	r.NoError(err)
	ti := exif.NewTagIndex()

	rootIb := exif.NewIfdBuilder(im, ti, exifcommon.IfdStandardIfdIdentity, binary.LittleEndian)
	r.NoError(rootIb.AddStandardWithName("Make", "Canon"))
	r.NoError(rootIb.AddStandardWithName("Model", "EOS R5"))

	exifIb := exif.NewIfdBuilder(im, ti, exifcommon.IfdExifStandardIfdIdentity, binary.LittleEndian)
	r.NoError(exifIb.AddStandardWithName("LensModel", lens))
	r.NoError(rootIb.AddChildIb(exifIb))

	ibe := exif.NewIfdByteEncoder()
	out, err := ibe.EncodeToExif(rootIb)
	r.NoError(err)
	return out
}

// TestParseExifSurfacesLensModel locks in that parseExif reads the
// LensModel tag (Exif Photo IFD, 0xA434) and surfaces it on Metadata
// with surrounding whitespace trimmed.
func TestParseExifSurfacesLensModel(t *testing.T) {
	r := require.New(t)
	const lens = "EF 50mm f/1.8 STM"
	padded := "  " + lens + " "
	raw := buildExifWithLensModel(t, padded)

	md, err := parseExif(raw)
	r.NoError(err)
	r.Equal(lens, md.LensModel)
	// Sanity: the same parse path keeps Make/Model intact.
	r.Equal("Canon", md.Make)
	r.Equal("EOS R5", md.Model)
}

// TestParseExifAbsentLensModelStaysEmpty proves the LensModel field is
// not populated by surrounding tags or stale state when LensModel is
// absent from the input.
func TestParseExifAbsentLensModelStaysEmpty(t *testing.T) {
	r := require.New(t)
	im, err := exifcommon.NewIfdMappingWithStandard()
	r.NoError(err)
	ti := exif.NewTagIndex()
	rootIb := exif.NewIfdBuilder(im, ti, exifcommon.IfdStandardIfdIdentity, binary.LittleEndian)
	r.NoError(rootIb.AddStandardWithName("Make", "Canon"))
	r.NoError(rootIb.AddStandardWithName("Model", "EOS R5"))
	ibe := exif.NewIfdByteEncoder()
	raw, err := ibe.EncodeToExif(rootIb)
	r.NoError(err)

	md, err := parseExif(raw)
	r.NoError(err)
	r.Empty(md.LensModel)
	r.Equal("Canon", md.Make)
}

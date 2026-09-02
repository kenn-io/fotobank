package media_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/media"
)

func TestProjectSourceMetadataSelectsPhotoFacts(t *testing.T) {
	r := require.New(t)
	iso := int64(400)
	width := int64(6240)
	height := int64(4160)
	aperture := 2.8
	focalLength := 50.0
	exposure := 0.008
	metadata := content.SourceMetadata{
		VersionID:            "0f5ce760-1855-4c9e-8ae8-13ec9af87463",
		ExtractorFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Checksum:             "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Fields: map[string]content.MetadataValue{
			"created": {Kind: "timestamp", Timestamp: &content.MetadataTimestamp{
				Normalized: "2024-06-15T14:30:22", Precision: "second", Timezone: "omitted",
			}},
			"image.exif.camera_make":           {Kind: "string", String: "Canon"},
			"image.exif.camera_model":          {Kind: "string", String: "EOS R5"},
			"image.exif.lens_model":            {Kind: "string", String: "RF50mm F1.2 L USM"},
			"image.exif.iso":                   {Kind: "integer", Integer: &iso},
			"image.exif.pixel_width":           {Kind: "integer", Integer: &width},
			"image.exif.pixel_height":          {Kind: "integer", Integer: &height},
			"image.exif.f_number":              {Kind: "number", Number: &aperture},
			"image.exif.focal_length_mm":       {Kind: "number", Number: &focalLength},
			"image.exif.exposure_time_seconds": {Kind: "number", Number: &exposure},
			"image.exif.gps_latitude":          {Kind: "string", String: "48.8566000"},
			"image.exif.gps_longitude":         {Kind: "string", String: "2.3522000"},
		},
	}

	projection, err := media.ProjectSourceMetadata(metadata, nil)
	r.NoError(err)
	r.Equal(metadata.VersionID, projection.VersionID)
	r.Equal(metadata.ExtractorFingerprint, projection.ExtractorFingerprint)
	r.Equal(metadata.Checksum, projection.Checksum)
	r.Equal(time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC), *projection.Timestamp)
	r.Equal("Canon", projection.Make)
	r.Equal("EOS R5", projection.Model)
	r.Equal("RF50mm F1.2 L USM", projection.LensModel)
	r.Equal("50", projection.FocalLength)
	r.Equal("0.008", projection.Shutter)
	r.Equal(6240, *projection.Width)
	r.Equal(4160, *projection.Height)
	r.Equal(400, *projection.ISO)
	r.InDelta(2.8, *projection.Aperture, 0)
	r.InDelta(48.8566, *projection.Latitude, 0)
	r.InDelta(2.3522, *projection.Longitude, 0)
}

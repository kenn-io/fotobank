package exifread_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/exifread"
)

func TestExtractPhotoParsesCoreFields(t *testing.T) {
	r := require.New(t)
	md, err := exifread.ExtractPhoto(filepath.Join("..", "..", "testdata", "exif", "photo-with-timestamp.jpg"))
	r.NoError(err)
	r.NotNil(md.Timestamp)
	// The fixture carries DateTimeOriginal = 2024-06-15 14:30:22 UTC.
	expected := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.True(md.Timestamp.Equal(expected), "got %v", md.Timestamp)
	r.NotEmpty(md.Make)
}

func TestExtractPhotoReturnsEmptyOnNoExif(t *testing.T) {
	r := require.New(t)
	md, err := exifread.ExtractPhoto(filepath.Join("..", "..", "testdata", "exif", "photo-no-exif.jpg"))
	r.NoError(err)
	r.Nil(md.Timestamp)
}

func TestExtractPhotoMissingFileReturnsError(t *testing.T) {
	r := require.New(t)
	_, err := exifread.ExtractPhoto("/no/such/file.jpg")
	r.Error(err)
}

func TestExtractPhotoParsesGPS(t *testing.T) {
	r := require.New(t)
	md, err := exifread.ExtractPhoto(filepath.Join("..", "..", "testdata", "exif", "photo-with-gps.jpg"))
	r.NoError(err)
	r.NotNil(md.Latitude)
	r.NotNil(md.Longitude)
	r.InDelta(48.8566, *md.Latitude, 1e-3)
	r.InDelta(2.3522, *md.Longitude, 1e-3)
	r.NotNil(md.GPSAt)
	expected := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.True(md.GPSAt.Equal(expected), "got %v", md.GPSAt)
}

func TestExtractPhotoDropsNullIslandGPS(t *testing.T) {
	r := require.New(t)
	md, err := exifread.ExtractPhoto(filepath.Join("..", "..", "testdata", "exif", "photo-null-island-gps.jpg"))
	r.NoError(err)
	r.Nil(md.Latitude)
	r.Nil(md.Longitude)
}

func TestExtractPhotoFromReaderMatchesPathVariant(t *testing.T) {
	r := require.New(t)
	path := filepath.Join("..", "..", "testdata", "exif", "photo-with-gps.jpg")
	md1, err := exifread.ExtractPhoto(path)
	r.NoError(err)

	f, err := os.Open(path)
	r.NoError(err)
	defer f.Close()
	md2, err := exifread.ExtractPhotoFromReader(f)
	r.NoError(err)

	// Both call paths funnel through the same exif decoder; the values
	// must be bit-identical, not merely close — hence delta=0.
	r.InDelta(*md1.Latitude, *md2.Latitude, 0)
	r.InDelta(*md1.Longitude, *md2.Longitude, 0)
}

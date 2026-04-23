package exifread_test

import (
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

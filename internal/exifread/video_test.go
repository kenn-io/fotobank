package exifread_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/exifread"
)

func TestExtractVideoMP4ParsesCreationTime(t *testing.T) {
	r := require.New(t)
	md, err := exifread.ExtractVideo(filepath.Join("..", "..", "testdata", "exif", "video.mp4"), "video/mp4")
	r.NoError(err)
	r.NotNil(md.Timestamp)
	expected := time.Date(2024, 6, 15, 14, 30, 22, 0, time.UTC)
	r.WithinDuration(expected, *md.Timestamp, time.Second)
	r.Equal(16, md.Width)
	r.Equal(16, md.Height)
	r.Greater(md.DurationMs, int64(500))
}

func TestExtractVideoUnknownFormatIsEmpty(t *testing.T) {
	// .avi is best-effort; any parse failure should yield empty
	// metadata with no error so import can still land the bytes.
	md, err := exifread.ExtractVideo(filepath.Join("..", "..", "testdata", "exif", "video.mp4"), "video/x-msvideo")
	require.NoError(t, err)
	require.Nil(t, md.Timestamp)
}

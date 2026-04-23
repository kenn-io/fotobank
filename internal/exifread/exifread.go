// Package exifread extracts normalised metadata from photo and video
// source files. Pure Go — no shell-out to exiftool or ffprobe.
package exifread

import (
	"time"
)

// Metadata is the normalised output of the EXIF/container extractors.
// All fields are zero/nil when not present in the source file; the
// import pipeline treats these as NULL in the media row.
type Metadata struct {
	// Common across photos and videos.
	Timestamp *time.Time
	Width     int
	Height    int

	// Photos.
	Make         string
	Model        string
	FocalLength  string
	ShutterSpeed string
	ISO          int
	Aperture     float64

	// RAW hint: an embedded JPEG preview is present and the thumb
	// worker should attempt to extract it (Plan C).
	HasEmbeddedPreview bool

	// Videos.
	DurationMs int64
}

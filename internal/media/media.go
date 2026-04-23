// Package media defines the in-memory media types and a SQLite-backed
// repository for the `media` table. A row represents a single photo or
// video that an owner has imported into this deployment.
package media

import (
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// Type is the discriminator between photo and video rows.
type Type string

const (
	TypePhoto Type = "photo"
	TypeVideo Type = "video"
)

// Media is the in-memory representation of a media row.
type Media struct {
	ID               string
	Owner            owners.Principal
	Type             Type
	MimeType         string
	Path             string
	OriginalFilename string
	ImportedAt       time.Time
	Timestamp        *time.Time
	Size             int64
	Checksum         string

	Make        string
	Model       string
	FocalLength string
	Shutter     string
	Width       *int
	Height      *int
	ISO         *int
	Aperture    *float64
	DurationMs  *int64

	ThumbStatus    string
	ThumbVersion   int
	ThumbUpdatedAt *time.Time
}

// ListFilter narrows the List query.
type ListFilter struct {
	Owner    owners.Principal
	Type     *Type
	DateFrom *time.Time
	DateTo   *time.Time
	Limit    int
	Offset   int
	// SortDesc sorts by timestamp desc when true; otherwise timestamp asc.
	SortDesc bool
}

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
	LensModel   string
	FocalLength string
	Shutter     string
	Width       *int
	Height      *int
	ISO         *int
	Aperture    *float64
	DurationMs  *int64

	// GPS metadata. Latitude and Longitude are atomic — emitted
	// together or not at all. GPSAt is independent. LocationLabel is
	// the offline-resolved country/region/city string from internal/geo;
	// empty when no resolver match.
	Latitude      *float64
	Longitude     *float64
	GPSAt         *time.Time
	LocationLabel string

	// F2.2 RAW+JPEG pairing.
	// Root-relative original path captured at import time. Substrate
	// for pair detection. Empty for pre-F2.2 dev rows.
	ImportSourcePath string
	// FK to the JPEG primary when this row is a sidecar. NULL on
	// primaries and standalones.
	PairedWithID *string

	ThumbStatus    string
	ThumbVersion   int
	ThumbUpdatedAt *time.Time

	// F2.4 Hidden privacy. NULL = visible; non-NULL = hidden, set when
	// the owner runs Hide. Cascades to sidecars via repo cascade methods;
	// service-layer Hide/Unhide rejects sidecar input ids directly.
	HiddenAt *time.Time
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
	// F2.2 RAW+JPEG pairing. False at the service layer by default;
	// service.MediaService.List clamps caller-supplied true values
	// back to false so no HTTP route can surface sidecars in lists.
	// Internal callers (pairing pass, backfill, reconcile) may set
	// true.
	IncludeSidecars bool
	// F2.4 Hidden privacy. When false (default), List appends
	// hidden_at IS NULL to the WHERE clause. Set true only by
	// internal callers that explicitly need hidden rows (e.g.
	// the hidden-management service layer).
	IncludeHidden bool
}

// ListGeoFilter scopes a ListGeo query.
type ListGeoFilter struct {
	Owner owners.Principal
	// IncludeHidden, when false (default), excludes rows whose
	// hidden_at IS NOT NULL. Set true only by callers that have
	// validated an unlock claim before calling.
	IncludeHidden bool
}

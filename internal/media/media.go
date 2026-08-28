// Package media defines Fotobank's product assets and their files.
package media

import (
	"time"

	"go.kenn.io/fotobank/internal/owners"
)

// Type is the discriminator between photo and video rows.
type Type string

const (
	TypePhoto Type = "photo"
	TypeVideo Type = "video"
)

// Media is the product-facing projection of a ready asset and its primary
// file. The public product still calls these items media; byte authority and
// file relationships live in Docbank and media_files respectively.
type Media struct {
	ID                 string
	Owner              owners.Principal
	Type               Type
	PrimaryFileID      string
	MimeType           string
	OriginalFilename   string
	ImportedAt         time.Time
	Timestamp          *time.Time
	Size               int64
	SHA256             string
	CurrentVersionID   string
	DocbankVirtualPath string

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

	ThumbStatus    string
	ThumbVersion   int
	ThumbUpdatedAt *time.Time

	// Hidden privacy. NULL = visible; non-NULL = hidden.
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
	// F2.4 Hidden privacy. When false (default), List appends
	// hidden_at IS NULL to the WHERE clause. Set true only by
	// internal callers that explicitly need hidden rows (e.g.
	// the hidden-management service layer).
	IncludeHidden bool
	// Cameras, when non-empty, narrows on (make || ' ' || model) matching
	// any of the values (OR-composed). Each value is the canonical
	// "<make> <model>" string used as both URL param and chip label.
	Cameras []string
	// Lenses, when non-empty, narrows on lens_model matching any of the
	// values (OR-composed).
	Lenses []string
	// AnyTagKeys, when non-empty, narrows to media that carry AT LEAST
	// ONE of the supplied tag keys (OR-composed via a single EXISTS).
	AnyTagKeys []string
	// HasGPS narrows on lat/lng presence. *true → both NOT NULL; *false →
	// at least one NULL; nil → no predicate.
	HasGPS *bool
}

// ListGeoFilter scopes a ListGeo query. ListGeo's contract is
// geotagged-only, so HasGPS is intentionally absent from this filter
// (every returned row already has lat and lon set).
type ListGeoFilter struct {
	Owner owners.Principal
	// IncludeHidden, when false (default), excludes rows whose
	// hidden_at IS NOT NULL. Set true only by callers that have
	// validated an unlock claim before calling.
	IncludeHidden bool
	// Type narrows on media_type. nil means both photo and video.
	Type *Type
	// Cameras, when non-empty, narrows on (make || ' ' || model)
	// matching any value (OR-composed). Each value is the canonical
	// "<make> <model>" string used as both URL param and chip label.
	Cameras []string
	// Lenses, when non-empty, narrows on lens_model matching any value
	// (OR-composed).
	Lenses []string
	// AnyTagKeys, when non-empty, narrows to media that carry AT LEAST
	// ONE of the supplied tag keys (OR-composed via a single EXISTS).
	AnyTagKeys []string
}

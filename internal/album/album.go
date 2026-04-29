// Package album defines the album and album_media domain types,
// validation sentinels, and size limits used by the album repo and
// service.
package album

import (
	"errors"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

type Album struct {
	ID        string
	Owner     owners.Principal
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CoverRef is the minimum the web app needs to construct
// /api/v1/media/{MediaID}/thumb?size=grid&v={ThumbVersion}.
type CoverRef struct {
	MediaID      string
	ThumbVersion int
}

// AlbumListItem is the shape returned by ListByOwner and GetDetailByID.
// ItemCount and Cover are derived in SQL so clients do not do N+1 calls.
type AlbumListItem struct {
	Album
	ItemCount int
	Cover     *CoverRef // nil when the album is empty OR has no ready thumb yet
}

// AlbumMediaFilter narrows ListMedia.
type AlbumMediaFilter struct {
	Limit  int
	Offset int
	// SortBy is "added" (default), "imported", or "taken". The service
	// rejects anything else with ErrInvalidSort. "taken" sorts by the
	// media row's EXIF timestamp with NULLS LAST in both directions.
	SortBy string
	// SortAsc inverts the default DESC sort. A plain bool so the HTTP
	// layer can bind it directly from ?sort_asc=true without pointer
	// plumbing; omitted → DESC, which is what the UI wants.
	SortAsc bool
}

const (
	// NameMaxLen caps trimmed album names. Service validation enforces this.
	NameMaxLen = 200
	// BatchMaxLen caps deduped AddMedia batches. Service validation enforces this.
	BatchMaxLen = 500
)

var (
	ErrInvalidName  = errors.New("album: name must be 1..200 chars after trim")
	ErrInvalidBatch = errors.New("album: media_ids batch must be 1..500")
	ErrInvalidSort  = errors.New("album: sort_by must be \"added\", \"imported\", or \"taken\"")
)

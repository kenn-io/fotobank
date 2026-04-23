// Package service — AlbumService is the auth-scoped entry point for
// album reads and writes. Every exported method takes caller
// owners.Principal; cross-owner access returns errs.ErrNotFound rather
// than ErrPermissionDenied so the surface cannot be used to probe for
// other owners' album or media IDs.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
)

// AlbumService orchestrates album CRUD on top of album.Repo. The media
// repo is wired in now because later tasks (AddMedia / ListMedia) need
// it for owner-consistency checks and row materialisation.
type AlbumService struct {
	albums *album.Repo
	media  *media.Repo
	now    func() time.Time
}

// NewAlbumService constructs an AlbumService. The clock defaults to
// time.Now().UTC(); tests that need determinism can construct an
// instance directly.
func NewAlbumService(a *album.Repo, m *media.Repo) *AlbumService {
	return &AlbumService{albums: a, media: m, now: func() time.Time { return time.Now().UTC() }}
}

// Create persists a new album owned by caller and returns the initial
// list item (ItemCount=0, Cover=nil).
func (s *AlbumService) Create(
	ctx context.Context,
	caller owners.Principal,
	name string,
) (album.AlbumListItem, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > album.NameMaxLen {
		return album.AlbumListItem{}, album.ErrInvalidName
	}
	now := s.now()
	a := album.Album{
		ID:        uuid.NewString(),
		Owner:     caller,
		Name:      trimmed,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.albums.Insert(ctx, a); err != nil {
		return album.AlbumListItem{}, err
	}
	return album.AlbumListItem{Album: a, ItemCount: 0, Cover: nil}, nil
}

// Get returns the bare album if caller is its owner; ErrNotFound
// otherwise (both for missing rows and cross-owner access).
func (s *AlbumService) Get(
	ctx context.Context,
	id string,
	caller owners.Principal,
) (album.Album, error) {
	a, err := s.albums.GetByID(ctx, id)
	if err != nil {
		return album.Album{}, err
	}
	if a.Owner != caller {
		return album.Album{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
	}
	return a, nil
}

// GetDetail returns the album with derived ItemCount + Cover.
// ErrNotFound if the album is missing or cross-owner.
func (s *AlbumService) GetDetail(
	ctx context.Context,
	id string,
	caller owners.Principal,
) (album.AlbumListItem, error) {
	item, err := s.albums.GetDetailByID(ctx, id)
	if err != nil {
		return album.AlbumListItem{}, err
	}
	if item.Owner != caller {
		return album.AlbumListItem{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
	}
	return item, nil
}

// Rename validates the new name, updates the row, and returns the fresh
// detail (ItemCount + Cover computed after the update).
// ErrNotFound if the album is missing or cross-owner.
func (s *AlbumService) Rename(
	ctx context.Context,
	id, name string,
	caller owners.Principal,
) (album.AlbumListItem, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > album.NameMaxLen {
		return album.AlbumListItem{}, album.ErrInvalidName
	}
	// Cheap owner check before the UPDATE.
	if _, err := s.Get(ctx, id, caller); err != nil {
		return album.AlbumListItem{}, err
	}
	if err := s.albums.Rename(ctx, id, trimmed, s.now()); err != nil {
		return album.AlbumListItem{}, err
	}
	return s.albums.GetDetailByID(ctx, id)
}

// Delete removes the album. The album_media rows are cascaded by the
// FK. ErrNotFound if the album is missing or cross-owner.
func (s *AlbumService) Delete(
	ctx context.Context,
	id string,
	caller owners.Principal,
) error {
	if _, err := s.Get(ctx, id, caller); err != nil {
		return err
	}
	return s.albums.Delete(ctx, id)
}

// List returns the caller's albums with derived ItemCount + Cover.
func (s *AlbumService) List(
	ctx context.Context,
	caller owners.Principal,
	limit, offset int,
) ([]album.AlbumListItem, error) {
	return s.albums.ListByOwner(ctx, caller, limit, offset)
}

// AddMedia validates the album is caller-owned, deduplicates input IDs
// (preserving first-seen order), length-checks the deduped batch, then
// performs a per-ID pre-flight ownership check via media.Repo.GetByID
// before the batched INSERT.
//
// Errors:
//   - errs.ErrNotFound if the album is missing or cross-owner, OR if any
//     media_id is missing, OR if any media_id belongs to a different
//     owner. The three are indistinguishable by design — a user who does
//     not own a media row must not learn whether it exists.
//   - album.ErrInvalidBatch if the deduped batch is empty or > 500.
//
// errs.ErrOwnerMismatch is reserved for the defence-in-depth path: it
// only surfaces if the trigger fires, which means this pre-flight missed
// a row. The HTTP layer maps that to 500, not 403.
func (s *AlbumService) AddMedia(
	ctx context.Context,
	albumID string,
	mediaIDs []string,
	caller owners.Principal,
) (added, alreadyPresent int, err error) {
	// Owner check on the album.
	if _, err := s.Get(ctx, albumID, caller); err != nil {
		return 0, 0, err
	}

	deduped := dedupeStrings(mediaIDs)
	if len(deduped) == 0 || len(deduped) > album.BatchMaxLen {
		return 0, 0, album.ErrInvalidBatch
	}

	for _, mid := range deduped {
		m, mErr := s.media.GetByID(ctx, mid)
		if mErr != nil {
			if errors.Is(mErr, errs.ErrNotFound) {
				return 0, 0, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, mid)
			}
			return 0, 0, mErr
		}
		if m.Owner != caller {
			// Mask cross-owner as not-found (existence-oracle protection).
			return 0, 0, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, mid)
		}
	}

	return s.albums.AddMedia(ctx, albumID, deduped, s.now())
}

// dedupeStrings returns ids with later duplicates removed, preserving
// first-seen order. Empty input returns a nil slice.
func dedupeStrings(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// RemoveMedia removes one media row from an album. ErrNotFound if the
// album is missing/cross-owner, or if (album, media) is not present.
func (s *AlbumService) RemoveMedia(
	ctx context.Context,
	albumID, mediaID string,
	caller owners.Principal,
) error {
	if _, err := s.Get(ctx, albumID, caller); err != nil {
		return err
	}
	return s.albums.RemoveMedia(ctx, albumID, mediaID)
}

// ListMedia returns paginated media rows for an album. The caller's
// ownership is checked via the album's owner; cross-owner returns
// ErrNotFound. SortBy is validated here so the repo never sees invalid
// values.
func (s *AlbumService) ListMedia(
	ctx context.Context,
	albumID string,
	filter album.AlbumMediaFilter,
	caller owners.Principal,
) ([]media.Media, error) {
	switch filter.SortBy {
	case "", "added", "imported":
		// ok
	default:
		return nil, album.ErrInvalidSort
	}
	if _, err := s.Get(ctx, albumID, caller); err != nil {
		return nil, err
	}
	return s.albums.ListMedia(ctx, albumID, filter)
}

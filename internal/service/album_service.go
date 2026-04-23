// Package service — AlbumService is the auth-scoped entry point for
// album reads and writes. Every exported method takes caller
// owners.Principal; cross-owner access returns errs.ErrNotFound rather
// than ErrPermissionDenied so the surface cannot be used to probe for
// other owners' album or media IDs.
package service

import (
	"context"
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

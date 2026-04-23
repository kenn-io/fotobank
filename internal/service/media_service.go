package service

import (
	"context"
	"fmt"
	"io"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/storage"
)

// MediaService brokers media reads for HTTP handlers. It enforces Plan
// B visibility: callers can only see media they own. When the caller
// is not the owner of a requested row, the service returns
// errs.ErrNotFound rather than ErrPermissionDenied so the handler
// surface cannot be used to probe for the existence of other owners'
// media.
type MediaService struct {
	repo  *media.Repo
	store storage.Store
}

// NewMediaService constructs a MediaService backed by repo and store.
func NewMediaService(repo *media.Repo, store storage.Store) *MediaService {
	return &MediaService{repo: repo, store: store}
}

// Get returns the media row identified by id when caller is its owner.
// Returns errs.ErrNotFound if no such row exists or if the caller is
// not the owner.
func (s *MediaService) Get(ctx context.Context, id string, caller owners.Principal) (media.Media, error) {
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return media.Media{}, err
	}
	if m.Owner != caller {
		return media.Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	return m, nil
}

// List returns media rows visible to caller. The filter's Owner is
// clamped to caller before delegating to the repo so a handler cannot
// request another owner's rows. Plan D will widen this to include
// shares.
func (s *MediaService) List(ctx context.Context, f media.ListFilter, caller owners.Principal) ([]media.Media, error) {
	f.Owner = caller
	return s.repo.List(ctx, f)
}

// StreamOriginal resolves the media row, enforces the owner check, and
// copies bytes from the backing store to w. offset and length follow
// the storage.Store.ReadRange convention: length < 0 means "to EOF".
// Returns the number of bytes written and any error. When the caller
// is not the owner, returns (0, errs.ErrNotFound) without touching w.
func (s *MediaService) StreamOriginal(
	ctx context.Context,
	id string,
	caller owners.Principal,
	offset, length int64,
	w io.Writer,
) (int64, error) {
	m, err := s.Get(ctx, id, caller)
	if err != nil {
		return 0, err
	}
	rc, err := s.store.ReadRange(ctx, caller, m.Path, offset, length)
	if err != nil {
		return 0, fmt.Errorf("read original: %w", err)
	}
	defer func() { _ = rc.Close() }()
	n, err := io.Copy(w, rc)
	if err != nil {
		return n, fmt.Errorf("stream original: %w", err)
	}
	return n, nil
}

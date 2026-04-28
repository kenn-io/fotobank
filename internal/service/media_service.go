package service

import (
	"context"
	"fmt"
	"io"
	"time"

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
// request another owner's rows. IncludeSidecars is also clamped to
// false so no HTTP route can surface sidecars in list responses; the
// flag is reserved for internal repo-layer callers (pairing pass,
// backfill CLI, reconcile). Plan D will widen this to include shares.
func (s *MediaService) List(ctx context.Context, f media.ListFilter, caller owners.Principal) ([]media.Media, error) {
	f.Owner = caller
	f.IncludeSidecars = false
	return s.repo.List(ctx, f)
}

// UpdateGPS persists the four GPS columns on a row owned by caller.
// The owner check goes through Get, which returns errs.ErrNotFound on
// caller mismatch — preserving the anti-probing convention. The CLI
// orchestrates "open NAS bytes, run exifread, resolve label" itself;
// the service layer stays simple and auth-scoped. Returns
// errs.ErrInvalidArgument (from the repo) if exactly one of lat/lon is
// set — the GPS coordinate pair is atomic.
func (s *MediaService) UpdateGPS(
	ctx context.Context,
	caller owners.Principal,
	id string,
	lat, lon *float64,
	gpsAt *time.Time,
	label string,
) error {
	if _, err := s.Get(ctx, id, caller); err != nil {
		return err
	}
	return s.repo.UpdateGPS(ctx, id, lat, lon, gpsAt, label)
}

// OpenOriginal resolves the media row, enforces the owner check, and
// returns the backing-store reader sliced by offset / length. offset and
// length follow the storage.Store.ReadRange convention: length < 0 means
// "to EOF". When the caller is not the owner, returns
// (nil, media.Media{}, errs.ErrNotFound). The caller owns the returned
// ReadCloser and must Close it.
func (s *MediaService) OpenOriginal(
	ctx context.Context,
	id string,
	caller owners.Principal,
	offset, length int64,
) (io.ReadCloser, media.Media, error) {
	m, err := s.Get(ctx, id, caller)
	if err != nil {
		return nil, media.Media{}, err
	}
	rc, err := s.store.ReadRange(ctx, caller, m.Path, offset, length)
	if err != nil {
		return nil, media.Media{}, fmt.Errorf("read original: %w", err)
	}
	return rc, m, nil
}

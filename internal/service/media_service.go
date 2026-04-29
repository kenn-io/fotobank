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

// HiddenBulkFailure describes a single per-id failure from a bulk Hide
// or Unhide operation.
type HiddenBulkFailure struct {
	ID   string `json:"id"`
	Code string `json:"code"` // not_found | invalid_sidecar
}

// HiddenBulkResult is returned by Hide and Unhide. Succeeded holds ids
// that were processed; Failed holds ids that were rejected before the
// cascade, with a per-id code.
type HiddenBulkResult struct {
	Succeeded []string
	Failed    []HiddenBulkFailure
}

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
// Returns errs.ErrNotFound if no such row exists, if the caller is not
// the owner, or if the row is hidden and includeHidden is not true.
// The variadic includeHidden keeps existing call sites compiling without
// change; pass true to allow returning hidden rows.
func (s *MediaService) Get(
	ctx context.Context,
	id string,
	caller owners.Principal,
	includeHidden ...bool,
) (media.Media, error) {
	wantHidden := len(includeHidden) > 0 && includeHidden[0]
	m, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return media.Media{}, err
	}
	if m.Owner != caller {
		return media.Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	if m.HiddenAt != nil && !wantHidden {
		return media.Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	return m, nil
}

// List returns media rows visible to caller. The filter's Owner is
// clamped to caller before delegating to the repo so a handler cannot
// request another owner's rows. IncludeSidecars and IncludeHidden are
// both clamped to false so no HTTP route can surface sidecars or hidden
// rows in list responses; those flags are reserved for internal repo-
// layer callers (pairing pass, backfill CLI, reconcile, hidden list).
func (s *MediaService) List(ctx context.Context, f media.ListFilter, caller owners.Principal) ([]media.Media, error) {
	f.Owner = caller
	f.IncludeSidecars = false
	f.IncludeHidden = false
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

// GetSidecars returns the sidecars of the primary identified by
// primaryID. The owner check goes through Get, which returns
// errs.ErrNotFound on caller mismatch — so a caller that does not own
// the primary cannot enumerate its sidecars. Returns an empty slice
// (not an error) when the primary has no sidecars.
func (s *MediaService) GetSidecars(
	ctx context.Context,
	primaryID string,
	caller owners.Principal,
) ([]media.Media, error) {
	if _, err := s.Get(ctx, primaryID, caller); err != nil {
		return nil, err
	}
	return s.repo.GetSidecars(ctx, primaryID)
}

// OpenOriginal resolves the media row, enforces the owner check, and
// returns the backing-store reader sliced by offset / length. offset and
// length follow the storage.Store.ReadRange convention: length < 0 means
// "to EOF". When the caller is not the owner or the row is hidden and
// includeHidden is not true, returns (nil, media.Media{}, errs.ErrNotFound).
// The variadic includeHidden keeps existing call sites compiling.
// The caller owns the returned ReadCloser and must Close it.
func (s *MediaService) OpenOriginal(
	ctx context.Context,
	id string,
	caller owners.Principal,
	offset, length int64,
	includeHidden ...bool,
) (io.ReadCloser, media.Media, error) {
	m, err := s.Get(ctx, id, caller, includeHidden...)
	if err != nil {
		return nil, media.Media{}, err
	}
	rc, err := s.store.ReadRange(ctx, caller, m.Path, offset, length)
	if err != nil {
		return nil, media.Media{}, fmt.Errorf("read original: %w", err)
	}
	return rc, m, nil
}

// Hide marks the given primary/standalone ids as hidden, cascading to
// sidecars. Input ids that are sidecars themselves are rejected with
// code "invalid_sidecar"; ids not owned by caller (or not found) are
// rejected with code "not_found". Returns a partial result; the error
// return is only non-nil for unexpected failures.
func (s *MediaService) Hide(
	ctx context.Context,
	caller owners.Principal,
	ids []string,
) (HiddenBulkResult, error) {
	return s.bulkHideOp(ctx, caller, ids, true)
}

// Unhide clears the hidden flag on the given ids, cascading to sidecars.
// Same validation rules as Hide apply. Returns a partial result.
func (s *MediaService) Unhide(
	ctx context.Context,
	caller owners.Principal,
	ids []string,
) (HiddenBulkResult, error) {
	return s.bulkHideOp(ctx, caller, ids, false)
}

// bulkHideOp validates the ids then calls the repo cascade. hide=true
// hides; hide=false unhides.
func (s *MediaService) bulkHideOp(
	ctx context.Context,
	caller owners.Principal,
	ids []string,
	hide bool,
) (HiddenBulkResult, error) {
	var result HiddenBulkResult
	if len(ids) == 0 {
		result.Succeeded = []string{}
		result.Failed = []HiddenBulkFailure{}
		return result, nil
	}
	result.Succeeded = make([]string, 0, len(ids))
	result.Failed = make([]HiddenBulkFailure, 0)

	var valid []string
	for _, id := range ids {
		// Fetch including hidden so we can validate ownership and sidecar
		// status regardless of current visibility.
		m, err := s.repo.GetByID(ctx, id)
		if err != nil {
			result.Failed = append(result.Failed, HiddenBulkFailure{ID: id, Code: "not_found"})
			continue
		}
		if m.Owner != caller {
			result.Failed = append(result.Failed, HiddenBulkFailure{ID: id, Code: "not_found"})
			continue
		}
		if m.PairedWithID != nil {
			result.Failed = append(result.Failed, HiddenBulkFailure{ID: id, Code: "invalid_sidecar"})
			continue
		}
		valid = append(valid, id)
	}

	if len(valid) > 0 {
		var opErr error
		if hide {
			opErr = s.repo.SetHiddenCascade(ctx, caller, valid, time.Now().UTC())
		} else {
			opErr = s.repo.ClearHiddenCascade(ctx, caller, valid)
		}
		if opErr != nil {
			return result, opErr
		}
		result.Succeeded = append(result.Succeeded, valid...)
	}
	return result, nil
}

// ClearAllHiddenForOwner delegates to the repo, clearing hidden_at on
// every row for owner. Used by hidden.Service.Disable via the
// MediaPrivacy interface.
func (s *MediaService) ClearAllHiddenForOwner(
	ctx context.Context,
	owner owners.Principal,
) error {
	return s.repo.ClearAllHiddenForOwner(ctx, owner)
}

// ListHidden returns hidden primary/standalone rows for caller, paginated
// by limit/offset. Sidecars are excluded (they cascade with their primary).
func (s *MediaService) ListHidden(
	ctx context.Context,
	caller owners.Principal,
	limit, offset int,
) ([]media.Media, error) {
	return s.repo.ListHidden(ctx, caller, limit, offset)
}

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/search/index"
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
	repo     *media.Repo
	resolver *contentresolver.Resolver
}

// NewMediaService constructs a MediaService backed by the product repository
// and exact-version resolver.
func NewMediaService(repo *media.Repo, resolver *contentresolver.Resolver) *MediaService {
	return &MediaService{repo: repo, resolver: resolver}
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
// request another owner's rows. Hidden rows are excluded from ordinary
// product lists.
func (s *MediaService) List(ctx context.Context, f media.ListFilter, caller owners.Principal) ([]media.Media, error) {
	f.Owner = caller
	f.IncludeHidden = false
	return s.repo.List(ctx, f)
}

// UpdateGPS persists the four GPS columns on a row owned by caller.
// The owner check goes through Get, which returns errs.ErrNotFound on
// caller mismatch — preserving the anti-probing convention. The CLI
// orchestrates exact-version metadata projection and location resolution;
// the service layer stays simple and auth-scoped. Returns
// errs.ErrInvalidArgument (from the repo) if exactly one of lat/lon is
// set — the GPS coordinate pair is atomic.
//
// The UPDATE and the media_fts refresh run in the same write
// transaction so search reads always see the location_label that the
// row carries; a mid-update failure rolls both writes back together.
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
	return s.repo.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if err := s.repo.UpdateGPSTx(ctx, tx, id, lat, lon, gpsAt, label); err != nil {
			return err
		}
		return index.RefreshMediaFTS(ctx, tx, id)
	})
}

// ListFiles returns the non-primary files belonging to an asset after
// enforcing ownership and visibility through the product media item.
func (s *MediaService) ListFiles(
	ctx context.Context,
	primaryID string,
	caller owners.Principal,
	includeHidden ...bool,
) ([]media.File, error) {
	if _, err := s.Get(ctx, primaryID, caller, includeHidden...); err != nil {
		return nil, err
	}
	files, err := s.repo.ListFiles(ctx, primaryID)
	if err != nil {
		return nil, err
	}
	out := make([]media.File, 0)
	for _, file := range files {
		if file.Role == media.RoleSidecar || file.Role == media.RoleOriginal {
			out = append(out, file)
		}
	}
	return out, nil
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
	ref, err := s.resolveContent(ctx, id, "", caller, includeHidden...)
	if err != nil {
		return nil, media.Media{}, err
	}
	opened, err := s.resolver.Open(ctx, ref, offset, length)
	if err != nil {
		return nil, media.Media{}, fmt.Errorf("read original: %w", err)
	}
	return opened.Reader, ref.Asset, nil
}

// GetFile enforces asset ownership and visibility and returns one file only
// when it belongs to that asset.
func (s *MediaService) GetFile(
	ctx context.Context,
	assetID string,
	fileID string,
	caller owners.Principal,
	includeHidden ...bool,
) (media.File, media.Media, error) {
	ref, err := s.resolveContent(ctx, assetID, fileID, caller, includeHidden...)
	if err != nil {
		return media.File{}, media.Media{}, err
	}
	return ref.File, ref.Asset, nil
}

// OpenFile opens one authorized asset file at its exact immutable Docbank
// version.
func (s *MediaService) OpenFile(
	ctx context.Context,
	assetID string,
	fileID string,
	caller owners.Principal,
	offset, length int64,
	includeHidden ...bool,
) (io.ReadCloser, media.File, media.Media, error) {
	ref, err := s.resolveContent(ctx, assetID, fileID, caller, includeHidden...)
	if err != nil {
		return nil, media.File{}, media.Media{}, err
	}
	opened, err := s.resolver.Open(ctx, ref, offset, length)
	if err != nil {
		return nil, media.File{}, media.Media{}, fmt.Errorf("read asset file: %w", err)
	}
	return opened.Reader, ref.File, ref.Asset, nil
}

func (s *MediaService) resolveContent(
	ctx context.Context,
	assetID, fileID string,
	caller owners.Principal,
	includeHidden ...bool,
) (contentresolver.Reference, error) {
	ref, err := s.resolver.ResolveCurrent(ctx, assetID, fileID)
	if err != nil {
		return contentresolver.Reference{}, err
	}
	wantHidden := len(includeHidden) > 0 && includeHidden[0]
	if ref.Asset.Owner != caller || (ref.Asset.HiddenAt != nil && !wantHidden) {
		return contentresolver.Reference{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, assetID)
	}
	return ref, nil
}

// Hide marks the given asset IDs as hidden. IDs not owned by caller (or
// not found) are rejected with code "not_found". Returns a partial result;
// the error return is only non-nil for unexpected failures.
func (s *MediaService) Hide(
	ctx context.Context,
	caller owners.Principal,
	ids []string,
) (HiddenBulkResult, error) {
	return s.bulkHideOp(ctx, caller, ids, true)
}

// Unhide clears the hidden flag on the given asset IDs.
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
			if !errors.Is(err, errs.ErrNotFound) {
				// Unexpected DB error — fail fast rather than silently treating
				// it as "not found" which would produce a partial-success lie.
				return result, fmt.Errorf("bulk hide op: fetch id=%s: %w", id, err)
			}
			result.Failed = append(result.Failed, HiddenBulkFailure{ID: id, Code: "not_found"})
			continue
		}
		if m.Owner != caller {
			result.Failed = append(result.Failed, HiddenBulkFailure{ID: id, Code: "not_found"})
			continue
		}
		valid = append(valid, id)
	}

	if len(valid) > 0 {
		var opErr error
		if hide {
			opErr = s.repo.SetHidden(ctx, caller, valid, time.Now().UTC())
		} else {
			opErr = s.repo.ClearHidden(ctx, caller, valid)
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

// ListGeoOptions narrows ListGeo's result set. /geo is geotagged-only
// by contract, so HasGPS is intentionally absent — every returned row
// already has lat and lon. The struct mirrors the SF-supported facet
// surface on /library and /search; an empty value (zero arrays, nil
// Type, IncludeHidden=false) means "no narrowing".
type ListGeoOptions struct {
	IncludeHidden bool
	Type          *media.Type
	Cameras       []string
	Lenses        []string
	AnyTagKeys    []string
}

// ListGeo returns geotagged primaries owned by caller. When
// opts.IncludeHidden is true, hidden rows are included; the handler is
// expected to have validated an unlock claim before calling.
//
// The service does not enforce the unlock-claim gate — the gate is in
// httpapi.registerMediaGeo so the 403 response shape stays inside the
// transport layer (matching list-hidden-media).
func (s *MediaService) ListGeo(
	ctx context.Context,
	caller owners.Principal,
	opts ListGeoOptions,
) ([]media.Media, error) {
	return s.repo.ListGeo(ctx, media.ListGeoFilter{
		Owner:         caller,
		IncludeHidden: opts.IncludeHidden,
		Type:          opts.Type,
		Cameras:       opts.Cameras,
		Lenses:        opts.Lenses,
		AnyTagKeys:    opts.AnyTagKeys,
	})
}

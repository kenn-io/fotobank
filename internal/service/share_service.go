package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
)

// ShareService is the auth-scoped entry point for scope mutations.
// Every exported method takes caller owners.Principal. Cross-owner
// access returns errs.ErrNotFound (never ErrOwnerMismatch), so the
// surface cannot be probed for other-owner scope UUIDs.
type ShareService struct {
	shares *share.Repo
	albums *album.Repo
	media  *media.Repo
	now    func() time.Time
	uuid   func() string
}

// NewShareService constructs a ShareService with production defaults.
func NewShareService(s *share.Repo, a *album.Repo, m *media.Repo) *ShareService {
	return &ShareService{
		shares: s, albums: a, media: m,
		now:  func() time.Time { return time.Now().UTC() },
		uuid: uuid.NewString,
	}
}

// CreateShareRequest is the service-level shape for Create.
type CreateShareRequest struct {
	Label         string
	Grantee       owners.Principal
	AllowDownload bool
	ExpiresAt     *time.Time
	TargetType    share.TargetType
	AlbumID       string
	MediaIDs      []string
}

// Create validates the request, pre-flights ownership on the target,
// and writes the scope row (+ scope_media for media_set) in one
// transaction. Returns the scope in its freshly-inserted StatusPending
// state.
func (s *ShareService) Create(ctx context.Context, req CreateShareRequest, caller owners.Principal) (share.Scope, error) {
	if len(req.Label) > share.LabelMaxLen {
		return share.Scope{}, share.ErrInvalidLabel
	}
	if !granteeValid(req.Grantee, caller) {
		return share.Scope{}, share.ErrInvalidGrantee
	}
	switch req.TargetType {
	case share.TargetAlbumLive:
		if req.AlbumID == "" || len(req.MediaIDs) > 0 {
			return share.Scope{}, share.ErrInvalidTargetCombo
		}
	case share.TargetMediaSet:
		if req.AlbumID != "" {
			return share.Scope{}, share.ErrInvalidTargetCombo
		}
	default:
		return share.Scope{}, share.ErrInvalidTargetCombo
	}

	scope := share.Scope{
		UUID:          s.uuid(),
		Owner:         caller,
		Grantee:       req.Grantee,
		TargetType:    req.TargetType,
		AllowDownload: req.AllowDownload,
		Label:         req.Label,
		ExpiresAt:     req.ExpiresAt,
		CreatedAt:     s.now(),
		BrokerStatus:  share.StatusPending,
	}

	var mediaIDs []string
	switch req.TargetType {
	case share.TargetAlbumLive:
		detail, err := s.albums.GetDetailByID(ctx, req.AlbumID)
		if err != nil {
			return share.Scope{}, err
		}
		if detail.Owner != caller {
			return share.Scope{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, req.AlbumID)
		}
		if detail.ItemCount == 0 {
			return share.Scope{}, share.ErrAlbumEmpty
		}
		id := detail.ID
		scope.TargetAlbumID = &id

	case share.TargetMediaSet:
		deduped, err := dedupeMediaIDs(req.MediaIDs)
		if err != nil {
			return share.Scope{}, err
		}
		for _, mid := range deduped {
			m, err := s.media.GetByID(ctx, mid)
			if err != nil {
				return share.Scope{}, err
			}
			if m.Owner != caller {
				return share.Scope{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, mid)
			}
		}
		mediaIDs = deduped
	}

	if err := s.shares.Insert(ctx, scope, mediaIDs); err != nil {
		return share.Scope{}, err
	}
	return scope, nil
}

// granteeValid enforces §8.2 grantee rules: non-empty hub and user_id,
// bounded length, not the same as caller. IsZero is implied by the
// non-empty check since IsZero == (hub == "" AND user_id == "").
func granteeValid(g, caller owners.Principal) bool {
	if g.Hub == "" || g.UserID == "" {
		return false
	}
	if len(g.Hub) > share.PrincipalFieldMaxLen || len(g.UserID) > share.PrincipalFieldMaxLen {
		return false
	}
	if g == caller {
		return false
	}
	return true
}

// dedupeMediaIDs removes duplicates (preserving first-seen order) and
// checks the bound. Returns ErrInvalidMediaSet if the deduped list is
// empty or exceeds share.MediaSetMaxLen.
func dedupeMediaIDs(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, share.ErrInvalidMediaSet
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 || len(out) > share.MediaSetMaxLen {
		return nil, share.ErrInvalidMediaSet
	}
	return out, nil
}

// Get returns the scope detail if caller is its owner, else
// errs.ErrNotFound. The scope's MediaIDs are populated for media_set
// targets.
func (s *ShareService) Get(ctx context.Context, uuidStr string, caller owners.Principal) (share.ScopeDetail, error) {
	det, err := s.shares.GetByUUID(ctx, uuidStr)
	if err != nil {
		return share.ScopeDetail{}, err
	}
	if det.Owner != caller {
		return share.ScopeDetail{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuidStr)
	}
	return det, nil
}

// List returns the caller's scopes. filter is passed through verbatim
// after Owner is forced to caller (defence-in-depth against a filter
// that set Grantee==caller or similar).
func (s *ShareService) List(ctx context.Context, filter share.ScopeFilter, caller owners.Principal) ([]share.Scope, error) {
	return s.shares.ListByOwner(ctx, caller, filter)
}

// Revoke marks the scope revoked locally and schedules broker
// revocation. Always idempotent from the worker's point of view; the
// service returns ErrScopeAlreadyRevoked if the row was already
// revoked (or has completed revoke).
func (s *ShareService) Revoke(ctx context.Context, uuidStr string, caller owners.Principal) (share.Scope, error) {
	det, err := s.shares.GetByUUID(ctx, uuidStr)
	if err != nil {
		return share.Scope{}, err
	}
	if det.Owner != caller {
		return share.Scope{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuidStr)
	}
	n, err := s.shares.SetRevoking(ctx, uuidStr, s.now())
	if err != nil {
		return share.Scope{}, err
	}
	if n == 0 {
		return share.Scope{}, share.ErrScopeAlreadyRevoked
	}
	fresh, err := s.shares.GetByUUID(ctx, uuidStr)
	if err != nil {
		return share.Scope{}, err
	}
	return fresh.Scope, nil
}

// Retry reopens a failed scope. Whether the retry routes through
// pending or revoking depends on whether the row was mid-publish or
// mid-revoke when it failed (encoded by revoked_at).
func (s *ShareService) Retry(ctx context.Context, uuidStr string, caller owners.Principal) (share.Scope, error) {
	det, err := s.shares.GetByUUID(ctx, uuidStr)
	if err != nil {
		return share.Scope{}, err
	}
	if det.Owner != caller {
		return share.Scope{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuidStr)
	}
	if det.BrokerStatus != share.StatusFailed {
		return share.Scope{}, share.ErrRetryNotApplicable
	}
	var n int64
	if det.RevokedAt == nil {
		n, err = s.shares.RetryPublish(ctx, uuidStr)
	} else {
		n, err = s.shares.RetryRevoke(ctx, uuidStr)
	}
	if err != nil {
		return share.Scope{}, err
	}
	if n == 0 {
		return share.Scope{}, share.ErrRetryNotApplicable
	}
	fresh, err := s.shares.GetByUUID(ctx, uuidStr)
	if err != nil {
		return share.Scope{}, err
	}
	return fresh.Scope, nil
}

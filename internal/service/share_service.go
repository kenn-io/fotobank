package service

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
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

// List returns the caller's scopes. filter is passed through verbatim;
// ownership is enforced by routing through share.Repo.ListByOwner(caller, …),
// so a caller cannot see another owner's scopes regardless of what the
// filter contains.
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

// ScopePreview is the materialised view PreviewScope returns. Media is
// the frozen membership for media_set and the live album_media order
// for album_live. Album is non-nil iff the scope's target_type is
// album_live. MediaIDs mirrors the existing /api/v1/shares/{uuid}
// detail shape — populated for media_set, empty for album_live.
type ScopePreview struct {
	Scope    share.Scope
	MediaIDs []string
	Media    []PreviewMedia
	Album    *share.AlbumSummary
	Warnings []string
}

// PreviewMedia is the owner-facing preview row for one media. Kept as
// a distinct type so the owner surface never accidentally reuses the
// grantee-side SharedMedia DTO.
type PreviewMedia struct {
	ID           string
	MediaType    media.Type
	MimeType     string
	DisplayTime  time.Time
	ThumbStatus  string
	ThumbVersion int
}

// PreviewScope returns the materialised view the grantee will see,
// gated to owner callers. Cross-owner UUIDs return errs.ErrNotFound
// (never ErrOwnerMismatch) so the preview surface cannot be probed for
// other owners' scope UUIDs. Uses share.Repo.ExpandScope so the
// grantee-side resolver is never invoked with owner-as-grantee
// semantics.
func (s *ShareService) PreviewScope(ctx context.Context, uuid string, caller owners.Principal) (ScopePreview, error) {
	exp, err := s.shares.ExpandScope(ctx, uuid)
	if err != nil {
		return ScopePreview{}, err
	}
	if exp.Scope.Owner != caller {
		return ScopePreview{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuid)
	}
	mediaRows, err := s.media.GetByIDs(ctx, exp.MediaIDs)
	if err != nil {
		return ScopePreview{}, err
	}
	out := ScopePreview{Scope: exp.Scope, Album: exp.Album}
	// Only media_set carries the frozen MediaIDs on the detail shape —
	// album_live's membership is live and exposed via Media[].
	if exp.Scope.TargetType == share.TargetMediaSet {
		out.MediaIDs = slices.Clone(exp.MediaIDs)
	}
	out.Media = make([]PreviewMedia, 0, len(mediaRows))
	for _, m := range mediaRows {
		display := m.ImportedAt
		if m.Timestamp != nil {
			display = *m.Timestamp
		}
		out.Media = append(out.Media, PreviewMedia{
			ID: m.ID, MediaType: m.Type, MimeType: m.MimeType,
			DisplayTime: display,
			ThumbStatus: m.ThumbStatus, ThumbVersion: m.ThumbVersion,
		})
	}
	out.Warnings = previewWarnings(exp, mediaRows, s.now())
	return out, nil
}

// previewWarnings reports a broker that is not active, expired scopes,
// empty-on-the-wire album_live scopes, and a
// "more than 25% of thumbs not ready" smoke signal.
func previewWarnings(exp share.ExpandedScope, mediaRows []media.Media, now time.Time) []string {
	var w []string
	if exp.Scope.BrokerStatus != share.StatusActive {
		w = append(w, "broker_not_active")
	}
	if exp.Scope.ExpiresAt != nil && !exp.Scope.ExpiresAt.After(now) {
		w = append(w, "scope_expired")
	}
	if exp.Scope.TargetType == share.TargetAlbumLive && exp.Album != nil && exp.Album.ItemCount == 0 {
		w = append(w, "empty_album")
	}
	if len(mediaRows) > 0 {
		missing := 0
		for _, m := range mediaRows {
			if m.ThumbStatus != "ready" {
				missing++
			}
		}
		if missing*100/len(mediaRows) > 25 {
			w = append(w, "missing_thumbs")
		}
	}
	return w
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

// PopulateTargetSummary builds a UI-friendly summary per scope. For
// album_live scopes the label is "Album: <name>" (or "Album: (deleted)"
// if the target_album_id no longer resolves) and ItemCount is nil. For
// media_set scopes the label is "N photos" (or "1 photo") and ItemCount
// carries the integer count. Album names and scope_media counts are
// fetched in two batched repo calls; scopes whose target_type is
// unknown are silently skipped. Empty input returns an empty (non-nil)
// map.
//
// The caller is expected to have already auth-scoped scopes; this
// helper does no owner check of its own. Used by HTTP list/detail
// handlers that have already gone through ShareService.List/Get.
func (s *ShareService) PopulateTargetSummary(
	ctx context.Context,
	scopes []share.Scope,
) (map[string]share.TargetSummary, error) {
	out := map[string]share.TargetSummary{}
	if len(scopes) == 0 {
		return out, nil
	}
	var albumIDs, setUUIDs []string
	for _, sc := range scopes {
		switch sc.TargetType {
		case share.TargetAlbumLive:
			if sc.TargetAlbumID != nil {
				albumIDs = append(albumIDs, *sc.TargetAlbumID)
			}
		case share.TargetMediaSet:
			setUUIDs = append(setUUIDs, sc.UUID)
		}
	}
	names, err := s.albums.GetNamesByIDs(ctx, albumIDs)
	if err != nil {
		return nil, fmt.Errorf("share: PopulateTargetSummary album names: %w", err)
	}
	counts, err := s.shares.CountSharedMediaByScopes(ctx, setUUIDs)
	if err != nil {
		return nil, fmt.Errorf("share: PopulateTargetSummary media counts: %w", err)
	}
	for _, sc := range scopes {
		switch sc.TargetType {
		case share.TargetAlbumLive:
			out[sc.UUID] = share.TargetSummary{Label: albumLiveLabel(sc, names)}
		case share.TargetMediaSet:
			n := counts[sc.UUID]
			out[sc.UUID] = share.TargetSummary{
				Label:     mediaSetLabel(n),
				ItemCount: &n,
			}
		}
	}
	return out, nil
}

// albumLiveLabel resolves the "Album: <name>" string. When the
// target_album_id is missing or no longer in the names map (album was
// deleted out from under the scope), it falls back to "Album: (deleted)"
// so the UI never renders a bare "Album:" prefix. Extracted from
// PopulateTargetSummary so the helper stays under the cyclomatic-
// complexity cap.
func albumLiveLabel(sc share.Scope, names map[string]string) string {
	if sc.TargetAlbumID == nil {
		return "Album: (deleted)"
	}
	if name, ok := names[*sc.TargetAlbumID]; ok {
		return "Album: " + name
	}
	return "Album: (deleted)"
}

// mediaSetLabel renders the "N photos" / "1 photo" string used by the
// scope target_summary. Singular vs plural is the only branch.
func mediaSetLabel(n int) string {
	if n == 1 {
		return "1 photo"
	}
	return strconv.Itoa(n) + " photos"
}

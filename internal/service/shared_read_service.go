package service

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/storage"
)

// SharedReadService is the auth boundary for every /api/v1/shared/*
// read. It depends on the resolver for authorisation and on the same
// repos the owner services use for data — but it never calls the owner
// services, which enforce caller == owner.
type SharedReadService struct {
	shares   *share.Repo
	media    *media.Repo
	albums   *album.Repo
	storage  storage.Store
	resolver *share.ScopeResolver
	now      func() time.Time
}

// NewSharedReadService constructs a SharedReadService with prod
// defaults.
func NewSharedReadService(
	shares *share.Repo,
	m *media.Repo,
	a *album.Repo,
	s storage.Store,
	r *share.ScopeResolver,
) *SharedReadService {
	return &SharedReadService{
		shares: shares, media: m, albums: a, storage: s, resolver: r,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// SharedScope is the caller's view of a scope granted to them.
type SharedScope struct {
	UUID          string
	TargetType    share.TargetType
	Owner         owners.Principal
	TargetAlbumID *string
	AllowDownload bool
	Label         string
	ExpiresAt     *time.Time
	ItemCount     int
}

// SharedScopeDetail is a SharedScope plus the media_set membership.
// MediaIDs is empty for album_live.
type SharedScopeDetail struct {
	SharedScope
	MediaIDs []string
}

// ListScopes returns the live, grantee-matched scopes carried in
// headerScopes. ItemCount is populated via CountSharedMediaByScope.
func (s *SharedReadService) ListScopes(
	ctx context.Context,
	caller owners.Principal,
	headerScopes []string,
) ([]SharedScope, error) {
	resolved, err := s.resolver.ResolveAll(ctx, caller, headerScopes)
	if err != nil {
		return nil, err
	}
	if len(resolved.Validated) == 0 {
		return nil, nil
	}
	slices.SortFunc(resolved.Validated, func(a, b share.Scope) int {
		return cmp.Compare(a.UUID, b.UUID)
	})
	out := make([]SharedScope, 0, len(resolved.Validated))
	for _, sc := range resolved.Validated {
		n, err := s.shares.CountSharedMediaByScope(ctx, sc.UUID)
		if err != nil {
			return nil, fmt.Errorf("count shared scope %s: %w", sc.UUID, err)
		}
		out = append(out, toSharedScope(sc, n))
	}
	return out, nil
}

// GetScope returns a single scope by UUID. The UUID must be present in
// headerScopes — a caller who has two scopes from the same owner cannot
// probe for a third by UUID.
func (s *SharedReadService) GetScope(
	ctx context.Context,
	caller owners.Principal,
	headerScopes []string,
	uuid string,
) (SharedScopeDetail, error) {
	if !slices.Contains(headerScopes, uuid) {
		return SharedScopeDetail{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuid)
	}
	resolved, err := s.resolver.ResolveAll(ctx, caller, headerScopes)
	if err != nil {
		return SharedScopeDetail{}, err
	}
	var match *share.Scope
	for i := range resolved.Validated {
		if resolved.Validated[i].UUID == uuid {
			match = &resolved.Validated[i]
			break
		}
	}
	if match == nil {
		return SharedScopeDetail{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuid)
	}
	detail, err := s.shares.GetByUUID(ctx, uuid)
	if err != nil {
		return SharedScopeDetail{}, err
	}
	// detail.MediaIDs is only populated for TargetMediaSet (the
	// frozen-at-mint set). For TargetAlbumLive, GetByUUID returns
	// an empty MediaIDs slice, so item_count is computed via the
	// scope_media / album_media coverage counter introduced in T6.
	count, err := s.shares.CountSharedMediaByScope(ctx, uuid)
	if err != nil {
		return SharedScopeDetail{}, err
	}
	out := SharedScopeDetail{SharedScope: toSharedScope(*match, count)}
	if match.TargetType == share.TargetMediaSet {
		out.MediaIDs = slices.Clone(detail.MediaIDs)
	}
	return out, nil
}

// toSharedScope projects a share.Scope + count into the grantee view.
func toSharedScope(s share.Scope, itemCount int) SharedScope {
	return SharedScope{
		UUID:          s.UUID,
		TargetType:    s.TargetType,
		Owner:         s.Owner,
		TargetAlbumID: s.TargetAlbumID,
		AllowDownload: s.AllowDownload,
		Label:         s.Label,
		ExpiresAt:     s.ExpiresAt,
		ItemCount:     itemCount,
	}
}

// SharedAlbumCover is the minimum a shared client needs to render
// /api/v1/shared/media/{id}/thumb. Cover existence implies a ready
// thumb.
type SharedAlbumCover struct {
	MediaID      string
	ThumbVersion int
}

// SharedAlbum is the grantee view of one album.
type SharedAlbum struct {
	ID          string
	Name        string
	Owner       owners.Principal
	ItemCount   int
	Cover       *SharedAlbumCover
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CanDownload bool
}

// SharedAlbumDetail mirrors SharedAlbum for now; kept distinct so the
// detail endpoint can add fields without widening the listing body.
type SharedAlbumDetail struct {
	SharedAlbum
}

// SharedMedia is the grantee view of one media row.
type SharedMedia struct {
	ID           string
	Owner        owners.Principal
	MediaType    media.Type
	MimeType     string
	DisplayTime  time.Time
	Width        *int
	Height       *int
	DurationMs   *int64
	ThumbStatus  string
	ThumbVersion int
	CanDownload  bool
}

// SharedMediaCursor paginates ListMedia / ListAlbumMedia.
type SharedMediaCursor struct {
	AfterDisplayTime time.Time
	AfterID          string
	Limit            int
}

const (
	sharedMediaDefaultLimit = 100
	sharedMediaMaxLimit     = 500
)

// clampSharedMediaLimit applies the default+max policy.
func clampSharedMediaLimit(n int) int {
	if n <= 0 {
		return sharedMediaDefaultLimit
	}
	if n > sharedMediaMaxLimit {
		return sharedMediaMaxLimit
	}
	return n
}

// ListAlbums returns distinct albums authorised by the album_live
// subset of headerScopes.
func (s *SharedReadService) ListAlbums(
	ctx context.Context,
	caller owners.Principal,
	headerScopes []string,
) ([]SharedAlbum, error) {
	resolved, err := s.resolver.ResolveAll(ctx, caller, headerScopes)
	if err != nil {
		return nil, err
	}
	rows, err := s.shares.ListSharedAlbumIDs(ctx, resolved.Validated, resolved.Owner)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	perm := make(map[string]bool, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		perm[row.AlbumID] = row.CanDownload
		ids = append(ids, row.AlbumID)
	}
	details, err := s.albums.GetDetailsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	// Sort details by updated_at DESC, id ASC for a stable listing order.
	// time.Time.Compare returns +1 when the receiver is later, so
	// b.UpdatedAt.Compare(a.UpdatedAt) returns +1 when b is later than a,
	// i.e. puts the later row first (DESC).
	slices.SortFunc(details, func(a, b album.AlbumListItem) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	out := make([]SharedAlbum, 0, len(details))
	for _, a := range details {
		out = append(out, toSharedAlbum(a, perm[a.ID]))
	}
	return out, nil
}

// GetAlbum returns album metadata iff a live album_live scope in
// headerScopes points at albumID.
func (s *SharedReadService) GetAlbum(
	ctx context.Context,
	caller owners.Principal,
	headerScopes []string,
	albumID string,
) (SharedAlbumDetail, error) {
	dec, err := s.resolver.CheckAlbumAccess(ctx, caller, headerScopes, albumID)
	if err != nil {
		return SharedAlbumDetail{}, err
	}
	if !dec.Authorized {
		return SharedAlbumDetail{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, albumID)
	}
	detail, err := s.albums.GetDetailByID(ctx, albumID)
	if err != nil {
		return SharedAlbumDetail{}, err
	}
	return SharedAlbumDetail{SharedAlbum: toSharedAlbum(detail, dec.CanDownload())}, nil
}

// ListAlbumMedia returns one page of an album's media, honouring the
// album's AccessDecision for CanDownload and the per-media OR with any
// covering media_set scopes.
//
// Note: CheckAlbumAccess and ResolveAll each run the resolver's
// validateAndRetain path, so a multi-owner presentation logs the spec
// §6.1 warn twice. Acceptable for this rare error case.
func (s *SharedReadService) ListAlbumMedia(
	ctx context.Context,
	caller owners.Principal,
	headerScopes []string,
	albumID string,
	cursor SharedMediaCursor,
) ([]SharedMedia, SharedMediaCursor, error) {
	dec, err := s.resolver.CheckAlbumAccess(ctx, caller, headerScopes, albumID)
	if err != nil {
		return nil, SharedMediaCursor{}, err
	}
	if !dec.Authorized {
		return nil, SharedMediaCursor{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, albumID)
	}
	resolved, err := s.resolver.ResolveAll(ctx, caller, headerScopes)
	if err != nil {
		return nil, SharedMediaCursor{}, err
	}
	limit := clampSharedMediaLimit(cursor.Limit)
	repoRows, err := s.shares.ListSharedMediaIDs(ctx, resolved.Validated, resolved.Owner, albumID,
		share.SharedMediaCursor{
			AfterDisplayTime: cursor.AfterDisplayTime,
			AfterID:          cursor.AfterID,
			Limit:            limit + 1,
		})
	if err != nil {
		return nil, SharedMediaCursor{}, err
	}
	page, next := pageSharedMediaRows(repoRows, limit)
	medias, err := s.fetchSharedMediaByRows(ctx, page)
	if err != nil {
		return nil, SharedMediaCursor{}, err
	}
	return medias, next, nil
}

// pageSharedMediaRows splits repo rows into (page, next-cursor) using
// the limit+1 convention.
func pageSharedMediaRows(rows []share.SharedMediaRow, limit int) ([]share.SharedMediaRow, SharedMediaCursor) {
	if len(rows) <= limit {
		return rows, SharedMediaCursor{}
	}
	last := rows[limit-1]
	return rows[:limit], SharedMediaCursor{
		AfterDisplayTime: last.DisplayTime,
		AfterID:          last.MediaID,
		Limit:            limit,
	}
}

// fetchSharedMediaByRows expands the repo rows into SharedMedia,
// preserving the input order and carrying the per-row CanDownload.
func (s *SharedReadService) fetchSharedMediaByRows(ctx context.Context, rows []share.SharedMediaRow) ([]SharedMedia, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	can := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MediaID)
		can[row.MediaID] = row.CanDownload
	}
	mediaRows, err := s.media.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]SharedMedia, 0, len(mediaRows))
	for _, m := range mediaRows {
		out = append(out, toSharedMedia(m, can[m.ID]))
	}
	return out, nil
}

// toSharedAlbum projects album.AlbumListItem + can_download into the
// shared view. ItemCount and Cover come straight from the detail row.
func toSharedAlbum(a album.AlbumListItem, canDownload bool) SharedAlbum {
	out := SharedAlbum{
		ID:          a.ID,
		Name:        a.Name,
		Owner:       a.Owner,
		ItemCount:   a.ItemCount,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
		CanDownload: canDownload,
	}
	if a.Cover != nil {
		out.Cover = &SharedAlbumCover{
			MediaID:      a.Cover.MediaID,
			ThumbVersion: a.Cover.ThumbVersion,
		}
	}
	return out
}

// toSharedMedia projects media.Media + can_download into the grantee
// view. display_time = COALESCE(timestamp, imported_at).
func toSharedMedia(m media.Media, canDownload bool) SharedMedia {
	display := m.ImportedAt
	if m.Timestamp != nil {
		display = *m.Timestamp
	}
	return SharedMedia{
		ID:           m.ID,
		Owner:        m.Owner,
		MediaType:    m.Type,
		MimeType:     m.MimeType,
		DisplayTime:  display,
		Width:        m.Width,
		Height:       m.Height,
		DurationMs:   m.DurationMs,
		ThumbStatus:  m.ThumbStatus,
		ThumbVersion: m.ThumbVersion,
		CanDownload:  canDownload,
	}
}

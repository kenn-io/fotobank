package service

import (
	"context"
	"fmt"
	"slices"
	"sort"
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
	sort.Slice(resolved.Validated, func(i, j int) bool {
		return resolved.Validated[i].UUID < resolved.Validated[j].UUID
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

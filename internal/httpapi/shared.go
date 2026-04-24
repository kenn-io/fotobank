package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
)

type sharedPrincipalDTO struct {
	Hub    string `json:"hub"`
	UserID string `json:"user_id"`
}

type sharedScopeDTO struct {
	UUID          string             `json:"uuid"`
	TargetType    string             `json:"target_type"`
	Owner         sharedPrincipalDTO `json:"owner"`
	TargetAlbumID string             `json:"target_album_id,omitempty"`
	AllowDownload bool               `json:"allow_download"`
	Label         string             `json:"label,omitempty"`
	ExpiresAt     *time.Time         `json:"expires_at,omitempty"`
	ItemCount     int                `json:"item_count,omitempty"`
	MediaIDs      []string           `json:"media_ids,omitempty"`
}

type sharedAlbumCoverDTO struct {
	MediaID      string `json:"media_id"`
	ThumbVersion int    `json:"thumb_version"`
}

type sharedAlbumDTO struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Owner       sharedPrincipalDTO   `json:"owner"`
	ItemCount   int                  `json:"item_count"`
	Cover       *sharedAlbumCoverDTO `json:"cover,omitempty"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
	CanDownload bool                 `json:"can_download"`
}

type sharedMediaDTO struct {
	ID           string             `json:"id"`
	Owner        sharedPrincipalDTO `json:"owner"`
	MediaType    string             `json:"media_type"`
	MimeType     string             `json:"mime_type"`
	DisplayTime  time.Time          `json:"display_time"`
	Size         int64              `json:"size,omitempty"`
	Width        *int               `json:"width,omitempty"`
	Height       *int               `json:"height,omitempty"`
	DurationMs   *int64             `json:"duration_ms,omitempty"`
	ThumbStatus  string             `json:"thumb_status"`
	ThumbVersion int                `json:"thumb_version"`
	CanDownload  bool               `json:"can_download"`
}

// translateSharedError maps SharedReadService errors onto the grantee
// surface's expected status codes. The only specialization over the
// cross-cutting Translate is ErrPermissionDenied → 403 with a
// download-specific message (OpenOriginal surfaces this when a scope
// authorises access but disables download); every other sentinel flows
// through to Translate.
func translateSharedError(err error) huma.StatusError {
	switch {
	case errors.Is(err, errs.ErrPermissionDenied):
		return huma.Error403Forbidden("download disabled for this share")
	default:
		return Translate(err)
	}
}

// registerShared wires /api/v1/shared/* read routes. svc == nil answers
// 503 so the OpenAPI dumper can build the spec without real deps.
func registerShared(api huma.API, svc *service.SharedReadService) {
	registerSharedListScopes(api, svc)
	registerSharedGetScope(api, svc)
	registerSharedListAlbums(api, svc)
	registerSharedGetAlbum(api, svc)
	registerSharedListAlbumMedia(api, svc)
	registerSharedListMedia(api, svc)
	registerSharedGetMedia(api, svc)
}

// sharedCall bundles the caller principal and header-attested scope
// UUIDs for a single request.
type sharedCall struct {
	caller owners.Principal
	scopes []string
}

func callerAndScopes(ctx context.Context) (sharedCall, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return sharedCall{}, errs.ErrIdentityMissing
	}
	return sharedCall{
		caller: id.Principal.OwnersPrincipal(),
		scopes: id.Scopes,
	}, nil
}

type sharedListScopesOutput struct {
	Body struct {
		Items []sharedScopeDTO `json:"items"`
	}
}

func registerSharedListScopes(api huma.API, svc *service.SharedReadService) {
	huma.Register(api, huma.Operation{
		OperationID: "shared-list-scopes",
		Method:      http.MethodGet,
		Path:        "/api/v1/shared/scopes",
	}, func(ctx context.Context, _ *struct{}) (*sharedListScopesOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("shared read unavailable")
		}
		call, err := callerAndScopes(ctx)
		if err != nil {
			return nil, translateSharedError(err)
		}
		rows, err := svc.ListScopes(ctx, call.caller, call.scopes)
		if err != nil {
			return nil, translateSharedError(err)
		}
		out := &sharedListScopesOutput{}
		out.Body.Items = make([]sharedScopeDTO, 0, len(rows))
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, toSharedScopeDTO(r, nil))
		}
		return out, nil
	})
}

type sharedScopeUUIDParam struct {
	UUID string `path:"uuid"`
}

type sharedScopeOutput struct {
	Body sharedScopeDTO
}

func registerSharedGetScope(api huma.API, svc *service.SharedReadService) {
	huma.Register(api, huma.Operation{
		OperationID: "shared-get-scope",
		Method:      http.MethodGet,
		Path:        "/api/v1/shared/scopes/{uuid}",
	}, func(ctx context.Context, in *sharedScopeUUIDParam) (*sharedScopeOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("shared read unavailable")
		}
		call, err := callerAndScopes(ctx)
		if err != nil {
			return nil, translateSharedError(err)
		}
		got, err := svc.GetScope(ctx, call.caller, call.scopes, in.UUID)
		if err != nil {
			return nil, translateSharedError(err)
		}
		return &sharedScopeOutput{Body: toSharedScopeDTO(got.SharedScope, got.MediaIDs)}, nil
	})
}

func toSharedScopeDTO(s service.SharedScope, mediaIDs []string) sharedScopeDTO {
	dto := sharedScopeDTO{
		UUID:          s.UUID,
		TargetType:    string(s.TargetType),
		Owner:         sharedPrincipalDTO{Hub: s.Owner.Hub, UserID: s.Owner.UserID},
		AllowDownload: s.AllowDownload,
		Label:         s.Label,
		ExpiresAt:     s.ExpiresAt,
		ItemCount:     s.ItemCount,
	}
	if s.TargetAlbumID != nil {
		dto.TargetAlbumID = *s.TargetAlbumID
	}
	if len(mediaIDs) > 0 {
		dto.MediaIDs = mediaIDs
	}
	return dto
}

type sharedListAlbumsOutput struct {
	Body struct {
		Items []sharedAlbumDTO `json:"items"`
	}
}

func registerSharedListAlbums(api huma.API, svc *service.SharedReadService) {
	huma.Register(api, huma.Operation{
		OperationID: "shared-list-albums",
		Method:      http.MethodGet,
		Path:        "/api/v1/shared/albums",
	}, func(ctx context.Context, _ *struct{}) (*sharedListAlbumsOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("shared read unavailable")
		}
		call, err := callerAndScopes(ctx)
		if err != nil {
			return nil, translateSharedError(err)
		}
		rows, err := svc.ListAlbums(ctx, call.caller, call.scopes)
		if err != nil {
			return nil, translateSharedError(err)
		}
		out := &sharedListAlbumsOutput{}
		out.Body.Items = make([]sharedAlbumDTO, 0, len(rows))
		for _, r := range rows {
			out.Body.Items = append(out.Body.Items, toSharedAlbumDTO(r))
		}
		return out, nil
	})
}

type sharedAlbumIDParam struct {
	ID string `path:"id"`
}

type sharedAlbumOutput struct {
	Body sharedAlbumDTO
}

func registerSharedGetAlbum(api huma.API, svc *service.SharedReadService) {
	huma.Register(api, huma.Operation{
		OperationID: "shared-get-album",
		Method:      http.MethodGet,
		Path:        "/api/v1/shared/albums/{id}",
	}, func(ctx context.Context, in *sharedAlbumIDParam) (*sharedAlbumOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("shared read unavailable")
		}
		call, err := callerAndScopes(ctx)
		if err != nil {
			return nil, translateSharedError(err)
		}
		got, err := svc.GetAlbum(ctx, call.caller, call.scopes, in.ID)
		if err != nil {
			return nil, translateSharedError(err)
		}
		return &sharedAlbumOutput{Body: toSharedAlbumDTO(got.SharedAlbum)}, nil
	})
}

func toSharedAlbumDTO(a service.SharedAlbum) sharedAlbumDTO {
	out := sharedAlbumDTO{
		ID:          a.ID,
		Name:        a.Name,
		Owner:       sharedPrincipalDTO{Hub: a.Owner.Hub, UserID: a.Owner.UserID},
		ItemCount:   a.ItemCount,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
		CanDownload: a.CanDownload,
	}
	if a.Cover != nil {
		out.Cover = &sharedAlbumCoverDTO{
			MediaID:      a.Cover.MediaID,
			ThumbVersion: a.Cover.ThumbVersion,
		}
	}
	return out
}

type sharedAlbumMediaInput struct {
	ID       string    `path:"id"`
	Limit    int       `query:"limit"`
	CursorTS time.Time `query:"cursor_time"`
	CursorID string    `query:"cursor_id"`
}

type sharedMediaListOutput struct {
	Body struct {
		Items          []sharedMediaDTO `json:"items"`
		NextCursorTime *time.Time       `json:"next_cursor_time,omitempty"`
		NextCursorID   string           `json:"next_cursor_id,omitempty"`
		HasMore        bool             `json:"has_more"`
	}
}

func registerSharedListAlbumMedia(api huma.API, svc *service.SharedReadService) {
	huma.Register(api, huma.Operation{
		OperationID: "shared-list-album-media",
		Method:      http.MethodGet,
		Path:        "/api/v1/shared/albums/{id}/media",
	}, func(ctx context.Context, in *sharedAlbumMediaInput) (*sharedMediaListOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("shared read unavailable")
		}
		call, err := callerAndScopes(ctx)
		if err != nil {
			return nil, translateSharedError(err)
		}
		if err := validateCursorPair(in.CursorTS, in.CursorID); err != nil {
			return nil, err
		}
		page, next, err := svc.ListAlbumMedia(ctx, call.caller, call.scopes, in.ID,
			service.SharedMediaCursor{
				AfterDisplayTime: in.CursorTS,
				AfterID:          in.CursorID,
				Limit:            in.Limit,
			})
		if err != nil {
			return nil, translateSharedError(err)
		}
		return buildSharedMediaListOutput(page, next), nil
	})
}

// validateCursorPair rejects cursors where exactly one half is present.
// A cursor with only display-time or only id cannot skip deterministically
// — the companion field is required for the tuple ordering used by
// share.Repo.ListSharedMediaIDs. Both absent (initial page) is fine.
func validateCursorPair(ts time.Time, id string) error {
	tsSet := !ts.IsZero()
	idSet := id != ""
	if tsSet != idSet {
		return huma.Error400BadRequest("cursor_time and cursor_id must be set together")
	}
	return nil
}

func buildSharedMediaListOutput(page []service.SharedMedia, next service.SharedMediaCursor) *sharedMediaListOutput {
	out := &sharedMediaListOutput{}
	out.Body.Items = make([]sharedMediaDTO, 0, len(page))
	for _, m := range page {
		out.Body.Items = append(out.Body.Items, toSharedMediaDTO(m))
	}
	if next.AfterID != "" {
		ts := next.AfterDisplayTime
		out.Body.NextCursorTime = &ts
		out.Body.NextCursorID = next.AfterID
		out.Body.HasMore = true
	}
	return out
}

func toSharedMediaDTO(m service.SharedMedia) sharedMediaDTO {
	return sharedMediaDTO{
		ID:           m.ID,
		Owner:        sharedPrincipalDTO{Hub: m.Owner.Hub, UserID: m.Owner.UserID},
		MediaType:    string(m.MediaType),
		MimeType:     m.MimeType,
		DisplayTime:  m.DisplayTime,
		Size:         m.Size,
		Width:        m.Width,
		Height:       m.Height,
		DurationMs:   m.DurationMs,
		ThumbStatus:  m.ThumbStatus,
		ThumbVersion: m.ThumbVersion,
		CanDownload:  m.CanDownload,
	}
}

type sharedListMediaInput struct {
	Limit    int       `query:"limit"`
	CursorTS time.Time `query:"cursor_time"`
	CursorID string    `query:"cursor_id"`
}

func registerSharedListMedia(api huma.API, svc *service.SharedReadService) {
	huma.Register(api, huma.Operation{
		OperationID: "shared-list-media",
		Method:      http.MethodGet,
		Path:        "/api/v1/shared/media",
	}, func(ctx context.Context, in *sharedListMediaInput) (*sharedMediaListOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("shared read unavailable")
		}
		call, err := callerAndScopes(ctx)
		if err != nil {
			return nil, translateSharedError(err)
		}
		if err := validateCursorPair(in.CursorTS, in.CursorID); err != nil {
			return nil, err
		}
		page, next, err := svc.ListMedia(ctx, call.caller, call.scopes,
			service.SharedMediaCursor{
				AfterDisplayTime: in.CursorTS,
				AfterID:          in.CursorID,
				Limit:            in.Limit,
			})
		if err != nil {
			return nil, translateSharedError(err)
		}
		return buildSharedMediaListOutput(page, next), nil
	})
}

type sharedMediaIDParam struct {
	ID string `path:"id"`
}

type sharedMediaOutput struct {
	Body sharedMediaDTO
}

func registerSharedGetMedia(api huma.API, svc *service.SharedReadService) {
	huma.Register(api, huma.Operation{
		OperationID: "shared-get-media",
		Method:      http.MethodGet,
		Path:        "/api/v1/shared/media/{id}",
	}, func(ctx context.Context, in *sharedMediaIDParam) (*sharedMediaOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("shared read unavailable")
		}
		call, err := callerAndScopes(ctx)
		if err != nil {
			return nil, translateSharedError(err)
		}
		got, err := svc.GetMedia(ctx, call.caller, call.scopes, in.ID)
		if err != nil {
			return nil, translateSharedError(err)
		}
		return &sharedMediaOutput{Body: toSharedMediaDTO(got)}, nil
	})
}

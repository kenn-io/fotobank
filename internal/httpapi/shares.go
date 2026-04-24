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
	"github.com/wesm/fotobank/internal/share"
)

// translateShareError maps service-layer errors to the share HTTP
// surface's expected status codes. Delegates unknown errors to the
// cross-cutting Translate.
func translateShareError(err error) huma.StatusError {
	switch {
	case errors.Is(err, errs.ErrOwnerMismatch):
		return huma.Error500InternalServerError(http.StatusText(http.StatusInternalServerError))
	case errors.Is(err, share.ErrAlbumHasLiveScopes):
		return huma.Error409Conflict("album has outstanding shares; revoke them first")
	case errors.Is(err, share.ErrScopeAlreadyRevoked):
		return huma.Error409Conflict("scope is already revoked")
	case errors.Is(err, share.ErrRetryNotApplicable):
		return huma.Error409Conflict("retry only applies to failed scopes")
	case errors.Is(err, share.ErrInvalidGrantee):
		return huma.Error400BadRequest("grantee hub/user_id must be non-empty, bounded, and not equal to caller")
	case errors.Is(err, share.ErrInvalidLabel):
		return huma.Error400BadRequest("label exceeds 200 chars")
	case errors.Is(err, share.ErrInvalidMediaSet):
		return huma.Error400BadRequest("media_ids must be 1..1000 unique ids")
	case errors.Is(err, share.ErrInvalidTargetCombo):
		return huma.Error400BadRequest("target_type does not match payload")
	case errors.Is(err, share.ErrAlbumEmpty):
		return huma.Error400BadRequest("cannot share an empty album")
	default:
		return Translate(err)
	}
}

const (
	sharesListDefaultLimit = 100
	sharesListMaxLimit     = 500
)

// principalDTO / scopeDTO / scopeDetailDTO are the wire shapes.
type principalDTO struct {
	Hub    string `json:"hub"`
	UserID string `json:"user_id"`
}

type scopeDTO struct {
	UUID                string       `json:"uuid"`
	Owner               principalDTO `json:"owner"`
	Grantee             principalDTO `json:"grantee"`
	TargetType          string       `json:"target_type"`
	TargetAlbumID       string       `json:"target_album_id,omitempty"`
	AllowDownload       bool         `json:"allow_download"`
	Label               string       `json:"label,omitempty"`
	CreatedAt           time.Time    `json:"created_at"`
	ExpiresAt           *time.Time   `json:"expires_at,omitempty"`
	Expired             bool         `json:"expired"`
	RevokedAt           *time.Time   `json:"revoked_at,omitempty"`
	BrokerStatus        string       `json:"broker_status"`
	BrokerRegisteredAt  *time.Time   `json:"broker_registered_at,omitempty"`
	BrokerGrantedAt     *time.Time   `json:"broker_granted_at,omitempty"`
	BrokerRevokedAt     *time.Time   `json:"broker_revoked_at,omitempty"`
	BrokerLastError     string       `json:"broker_last_error,omitempty"`
	BrokerAttempts      int          `json:"broker_attempts"`
	BrokerNextAttemptAt *time.Time   `json:"broker_next_attempt_at,omitempty"`
}

type scopeDetailDTO struct {
	scopeDTO
	MediaIDs []string `json:"media_ids,omitempty"`
}

func toScopeDTO(s share.Scope) scopeDTO {
	out := scopeDTO{
		UUID:                s.UUID,
		Owner:               principalDTO{Hub: s.Owner.Hub, UserID: s.Owner.UserID},
		Grantee:             principalDTO{Hub: s.Grantee.Hub, UserID: s.Grantee.UserID},
		TargetType:          string(s.TargetType),
		AllowDownload:       s.AllowDownload,
		Label:               s.Label,
		CreatedAt:           s.CreatedAt,
		ExpiresAt:           s.ExpiresAt,
		RevokedAt:           s.RevokedAt,
		BrokerStatus:        string(s.BrokerStatus),
		BrokerRegisteredAt:  s.BrokerRegisteredAt,
		BrokerGrantedAt:     s.BrokerGrantedAt,
		BrokerRevokedAt:     s.BrokerRevokedAt,
		BrokerLastError:     s.BrokerLastError,
		BrokerAttempts:      s.BrokerAttempts,
		BrokerNextAttemptAt: s.BrokerNextAttemptAt,
	}
	if s.TargetAlbumID != nil {
		out.TargetAlbumID = *s.TargetAlbumID
	}
	if s.ExpiresAt != nil && time.Now().UTC().After(*s.ExpiresAt) {
		out.Expired = true
	}
	return out
}

func toScopeDetailDTO(d share.ScopeDetail) scopeDetailDTO {
	return scopeDetailDTO{scopeDTO: toScopeDTO(d.Scope), MediaIDs: d.MediaIDs}
}

func callerFromCtx(ctx context.Context) (owners.Principal, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return owners.Principal{}, errs.ErrIdentityMissing
	}
	return id.Principal.OwnersPrincipal(), nil
}

// registerShares wires /api/v1/shares. svc == nil answers 503 so the
// OpenAPI dumper can build the spec without real deps.
func registerShares(api huma.API, svc *service.ShareService) {
	registerSharesCreate(api, svc)
	registerSharesList(api, svc)
	registerSharesGet(api, svc)
	registerSharesRevoke(api, svc)
	registerSharesRetry(api, svc)
}

// --- inputs/outputs ---

type createShareInput struct {
	Body struct {
		Label         string       `json:"label,omitempty"`
		Grantee       principalDTO `json:"grantee"`
		AllowDownload bool         `json:"allow_download,omitempty"`
		ExpiresAt     *time.Time   `json:"expires_at,omitempty"`
		TargetType    string       `json:"target_type"`
		AlbumID       string       `json:"album_id,omitempty"`
		MediaIDs      []string     `json:"media_ids,omitempty"`
	}
}

type scopeOutput struct {
	Status int
	Body   scopeDTO
}

type scopeDetailOutput struct {
	Status int
	Body   scopeDetailDTO
}

type listSharesInput struct {
	AlbumID        string `query:"album_id"`
	GranteeHub     string `query:"grantee_hub"`
	GranteeUserID  string `query:"grantee_user_id"`
	Status         string `query:"status" doc:"comma-separated broker_status filter"`
	IncludeSettled bool   `query:"include_settled"`
	Limit          int    `query:"limit" doc:"max rows (default 100, cap 500)"`
	Offset         int    `query:"offset"`
}

type listSharesOutput struct {
	Body struct {
		Items []scopeDTO `json:"items"`
	}
}

// --- handlers ---

func registerSharesCreate(api huma.API, svc *service.ShareService) {
	huma.Register(api, huma.Operation{
		OperationID:   "shares-create",
		Method:        http.MethodPost,
		Path:          "/api/v1/shares",
		Summary:       "Create a scope (share) over an album or media set",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createShareInput) (*scopeOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("share service unavailable")
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, translateShareError(err)
		}
		req := service.CreateShareRequest{
			Label:         in.Body.Label,
			Grantee:       owners.Principal{Hub: in.Body.Grantee.Hub, UserID: in.Body.Grantee.UserID},
			AllowDownload: in.Body.AllowDownload,
			ExpiresAt:     in.Body.ExpiresAt,
			TargetType:    share.TargetType(in.Body.TargetType),
			AlbumID:       in.Body.AlbumID,
			MediaIDs:      in.Body.MediaIDs,
		}
		s, err := svc.Create(ctx, req, caller)
		if err != nil {
			return nil, translateShareError(err)
		}
		return &scopeOutput{Status: http.StatusCreated, Body: toScopeDTO(s)}, nil
	})
}

func registerSharesList(api huma.API, svc *service.ShareService) {
	huma.Register(api, huma.Operation{
		OperationID: "shares-list",
		Method:      http.MethodGet,
		Path:        "/api/v1/shares",
	}, func(ctx context.Context, in *listSharesInput) (*listSharesOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("share service unavailable")
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, translateShareError(err)
		}
		statuses, err := share.ParseStatusFilter(in.Status)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		limit := clampLimit(in.Limit, sharesListDefaultLimit, sharesListMaxLimit)
		filter := share.ScopeFilter{
			AlbumID:        in.AlbumID,
			Grantee:        owners.Principal{Hub: in.GranteeHub, UserID: in.GranteeUserID},
			Status:         statuses,
			IncludeSettled: in.IncludeSettled,
			Limit:          limit,
			Offset:         in.Offset,
		}
		rows, err := svc.List(ctx, filter, caller)
		if err != nil {
			return nil, translateShareError(err)
		}
		out := &listSharesOutput{}
		out.Body.Items = make([]scopeDTO, 0, len(rows))
		for _, s := range rows {
			out.Body.Items = append(out.Body.Items, toScopeDTO(s))
		}
		return out, nil
	})
}

type scopeUUIDParam struct {
	UUID string `path:"uuid"`
}

func registerSharesGet(api huma.API, svc *service.ShareService) {
	huma.Register(api, huma.Operation{
		OperationID: "shares-get",
		Method:      http.MethodGet,
		Path:        "/api/v1/shares/{uuid}",
	}, func(ctx context.Context, in *scopeUUIDParam) (*scopeDetailOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("share service unavailable")
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, translateShareError(err)
		}
		det, err := svc.Get(ctx, in.UUID, caller)
		if err != nil {
			return nil, translateShareError(err)
		}
		return &scopeDetailOutput{Status: http.StatusOK, Body: toScopeDetailDTO(det)}, nil
	})
}

func registerSharesRevoke(api huma.API, svc *service.ShareService) {
	huma.Register(api, huma.Operation{
		OperationID: "shares-revoke",
		Method:      http.MethodPost,
		Path:        "/api/v1/shares/{uuid}/revoke",
	}, func(ctx context.Context, in *scopeUUIDParam) (*scopeOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("share service unavailable")
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, translateShareError(err)
		}
		s, err := svc.Revoke(ctx, in.UUID, caller)
		if err != nil {
			return nil, translateShareError(err)
		}
		return &scopeOutput{Status: http.StatusOK, Body: toScopeDTO(s)}, nil
	})
}

func registerSharesRetry(api huma.API, svc *service.ShareService) {
	huma.Register(api, huma.Operation{
		OperationID: "shares-retry",
		Method:      http.MethodPost,
		Path:        "/api/v1/shares/{uuid}/retry",
	}, func(ctx context.Context, in *scopeUUIDParam) (*scopeOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("share service unavailable")
		}
		caller, err := callerFromCtx(ctx)
		if err != nil {
			return nil, translateShareError(err)
		}
		s, err := svc.Retry(ctx, in.UUID, caller)
		if err != nil {
			return nil, translateShareError(err)
		}
		return &scopeOutput{Status: http.StatusOK, Body: toScopeDTO(s)}, nil
	})
}

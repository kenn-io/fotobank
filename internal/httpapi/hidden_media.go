package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/service"
)

// --- request / response types ---

type hiddenMediaBulkInput struct {
	Body struct {
		MediaIDs []string `json:"media_ids" doc:"IDs of primary or standalone rows to hide/unhide."`
	}
}

type hiddenMediaBulkFailureDTO struct {
	ID   string `json:"id"`
	Code string `json:"code"` // not_found | invalid_sidecar
}

type hiddenMediaBulkOutput struct {
	Body struct {
		Succeeded []string                    `json:"succeeded"`
		Failed    []hiddenMediaBulkFailureDTO `json:"failed"`
	}
}

type listHiddenMediaInput struct {
	Limit  int `query:"limit"  doc:"max rows to return (default 100, cap 1000)"`
	Offset int `query:"offset" doc:"pagination offset"`
}

type listHiddenMediaOutput struct {
	Body struct {
		Items      []MediaDTO `json:"items"`
		NextOffset *int       `json:"next_offset,omitempty"`
	}
}

// --- registration ---

// registerHiddenMedia registers:
//   - GET  /api/v1/hidden/media
//   - POST /api/v1/media/hidden:bulk
//   - POST /api/v1/media/unhide:bulk
//
// Note: colon-action paths were attempted first (/api/v1/media:hidden,
// /api/v1/media:unhide); huma/v2 with humago does not support colon-
// separated action suffixes at the path level, so the fallback form
// /api/v1/media/hidden:bulk and /api/v1/media/unhide:bulk is used.
// Both svc and hiddenAuth may be nil; handlers return 503 so the
// OpenAPI spec dumper still emits the route shapes.
func registerHiddenMedia(api huma.API, svc *service.MediaService, hiddenAuth *hidden.Service) {
	registerListHiddenMedia(api, svc, hiddenAuth)
	registerHideMediaBulk(api, svc, hiddenAuth)
	registerUnhideMediaBulk(api, svc, hiddenAuth)
}

func registerListHiddenMedia(
	api huma.API,
	svc *service.MediaService,
	hiddenAuth *hidden.Service,
) {
	huma.Register(api, huma.Operation{
		OperationID: "list-hidden-media",
		Method:      http.MethodGet,
		Path:        "/api/v1/hidden/media",
		Summary:     "List hidden media for the caller (requires unlock cookie)",
	}, func(ctx context.Context, in *listHiddenMediaInput) (*listHiddenMediaOutput, error) {
		if svc == nil || hiddenAuth == nil {
			return nil, huma.Error503ServiceUnavailable("hidden media service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		caller := id.Principal.OwnersPrincipal()
		// List requires the unlock cookie and returns 403 without it.
		// This differs from direct-by-id which returns 404 (anti-enumeration).
		claim, hasClaim := hidden.UnlockClaimFromContext(ctx)
		if !hasClaim || claim.Principal != caller {
			return nil, huma.Error403Forbidden("unlock cookie required")
		}

		limit := in.Limit
		if limit <= 0 {
			limit = listMediaDefaultLimit
		}
		if limit > listMediaMaxLimit {
			limit = listMediaMaxLimit
		}
		// Fetch one extra to determine next_offset.
		rows, err := svc.ListHidden(ctx, caller, limit+1, in.Offset)
		if err != nil {
			return nil, Translate(err)
		}
		out := &listHiddenMediaOutput{}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
			next := in.Offset + limit
			out.Body.NextOffset = &next
		}
		out.Body.Items = make([]MediaDTO, 0, len(rows))
		for _, m := range rows {
			out.Body.Items = append(out.Body.Items, toMediaDTO(m))
		}
		return out, nil
	})
}

func registerHideMediaBulk(
	api huma.API,
	svc *service.MediaService,
	hiddenAuth *hidden.Service,
) {
	huma.Register(api, huma.Operation{
		OperationID: "hide-media-bulk",
		Method:      http.MethodPost,
		Path:        "/api/v1/media/hidden:bulk",
		Summary:     "Bulk-hide media (requires hidden privacy to be configured; no unlock cookie needed)",
	}, func(ctx context.Context, in *hiddenMediaBulkInput) (*hiddenMediaBulkOutput, error) {
		if svc == nil || hiddenAuth == nil {
			return nil, huma.Error503ServiceUnavailable("hidden media service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		caller := id.Principal.OwnersPrincipal()
		// Hide requires a configured credential but NOT an unlock cookie.
		// If there's no credential, return 409; any other error is a real
		// failure and must not be masked as a 409.
		if _, err := hiddenAuth.GetCredential(ctx, caller); err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				return nil, huma.Error409Conflict(errs.ErrHiddenNotConfigured.Error())
			}
			return nil, Translate(err)
		}
		result, err := svc.Hide(ctx, caller, in.Body.MediaIDs)
		if err != nil {
			return nil, Translate(err)
		}
		return toBulkOutput(result), nil
	})
}

func registerUnhideMediaBulk(
	api huma.API,
	svc *service.MediaService,
	hiddenAuth *hidden.Service,
) {
	huma.Register(api, huma.Operation{
		OperationID: "unhide-media-bulk",
		Method:      http.MethodPost,
		Path:        "/api/v1/media/unhide:bulk",
		Summary:     "Bulk-unhide media (requires unlock cookie)",
	}, func(ctx context.Context, in *hiddenMediaBulkInput) (*hiddenMediaBulkOutput, error) {
		if svc == nil || hiddenAuth == nil {
			return nil, huma.Error503ServiceUnavailable("hidden media service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		caller := id.Principal.OwnersPrincipal()
		// Unhide requires the unlock cookie.
		claim, hasClaim := hidden.UnlockClaimFromContext(ctx)
		if !hasClaim || claim.Principal != caller {
			return nil, huma.Error403Forbidden("unlock cookie required")
		}
		result, err := svc.Unhide(ctx, caller, in.Body.MediaIDs)
		if err != nil {
			return nil, Translate(err)
		}
		return toBulkOutput(result), nil
	})
}

// toBulkOutput converts a service.HiddenBulkResult to the HTTP DTO.
func toBulkOutput(result service.HiddenBulkResult) *hiddenMediaBulkOutput {
	out := &hiddenMediaBulkOutput{}
	out.Body.Succeeded = result.Succeeded
	if len(result.Failed) == 0 {
		out.Body.Failed = []hiddenMediaBulkFailureDTO{}
	} else {
		out.Body.Failed = make([]hiddenMediaBulkFailureDTO, 0, len(result.Failed))
		for _, f := range result.Failed {
			out.Body.Failed = append(out.Body.Failed, hiddenMediaBulkFailureDTO{
				ID:   f.ID,
				Code: f.Code,
			})
		}
	}
	return out
}

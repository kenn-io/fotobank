package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/service"
)

type listMediaGeoInput struct {
	IncludeHidden bool `query:"include_hidden" doc:"Return hidden geotagged media; requires a valid hidden-unlock cookie."`
}

type listMediaGeoOutput struct {
	Body struct {
		Items []mediaDTO `json:"items"`
	}
}

// registerMediaGeo wires GET /api/v1/media/geo. Returns the full owner-
// scoped set of geotagged primaries (sidecars excluded). When the
// caller passes ?include_hidden=true, the handler validates the
// hidden-unlock cookie before delegating to the service.
//
// Both svc and hiddenAuth may be nil; when either is nil the handler
// answers 503 so the OpenAPI dumper can pass an empty Deps and still
// emit the route shape.
func registerMediaGeo(api huma.API, svc *service.MediaService, hiddenAuth *hidden.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "list-media-geo",
		Method:      http.MethodGet,
		Path:        "/api/v1/media/geo",
		Summary:     "List the caller's geotagged media (primaries only).",
	}, func(ctx context.Context, in *listMediaGeoInput) (*listMediaGeoOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("media service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		caller := id.Principal.OwnersPrincipal()

		if in.IncludeHidden {
			if hiddenAuth == nil {
				return nil, huma.Error503ServiceUnavailable("hidden media service not configured")
			}
			claim, hasClaim := hidden.UnlockClaimFromContext(ctx)
			if !hasClaim || claim.Principal != caller {
				return nil, huma.Error403Forbidden("unlock cookie required")
			}
		}

		rows, err := svc.ListGeo(ctx, caller, in.IncludeHidden)
		if err != nil {
			return nil, Translate(err)
		}
		out := &listMediaGeoOutput{}
		out.Body.Items = make([]mediaDTO, 0, len(rows))
		for _, m := range rows {
			out.Body.Items = append(out.Body.Items, toMediaDTO(m))
		}
		return out, nil
	})
}

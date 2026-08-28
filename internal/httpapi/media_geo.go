package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/service"
)

type listMediaGeoInput struct {
	IncludeHidden bool `query:"include_hidden" doc:"Return hidden geotagged media; requires a valid hidden-unlock cookie."`
	// has_gps is intentionally NOT exposed: /geo's contract is
	// geotagged-only, so every returned row already has lat/lon.
	Camera   []string `query:"camera,explode" doc:"Narrow to media whose canonical camera (\"<make> <model>\") matches any of these. OR-composed."`
	Lens     []string `query:"lens,explode"   doc:"Narrow to media whose lens_model matches any of these. OR-composed."`
	FacetTag []string `query:"facet_tag,explode" doc:"Narrow to media that carry at least one of the supplied tag keys. OR-composed."`
	// MediaType is a plain string with enum {"photo","video"}; "" means
	// "unset" (return both). huma v2 panics on *string query params, so
	// we mirror the /api/v1/media handler's pattern and decode the empty
	// string as the tri-state's null branch.
	MediaType string `query:"media_type" enum:"photo,video" doc:"Narrow to photo or video. Omit for both."`
}

type listMediaGeoOutput struct {
	Body struct {
		Items []mediaDTO `json:"items"`
	}
}

// registerMediaGeo wires GET /api/v1/media/geo. Returns the full owner-
// scoped set of geotagged ready assets. When the
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

		var mediaType *media.Type
		if in.MediaType != "" {
			t := media.Type(in.MediaType)
			mediaType = &t
		}
		rows, err := svc.ListGeo(ctx, caller, service.ListGeoOptions{
			IncludeHidden: in.IncludeHidden,
			Type:          mediaType,
			Cameras:       in.Camera,
			Lenses:        in.Lens,
			AnyTagKeys:    in.FacetTag,
		})
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

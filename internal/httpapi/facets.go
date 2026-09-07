// Package httpapi — facets surface. GET /api/v1/facets returns
// per-facet counts for the caller's library, computed under the
// exclude-self rule so each dropdown shows reachable alternatives.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/service/facets"
)

// registerFacetsRoutes binds GET /api/v1/facets. The IncludeHidden
// gate lives inside facets.Service.Aggregate (it's the only call site
// that mutates SQL based on IncludeHidden), so the route only needs
// the service handle — the unlock-claim check is plumbed into the
// service via facets.New, not threaded through here.
func registerFacetsRoutes(api huma.API, svc *facets.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "facets",
		Method:      http.MethodGet,
		Path:        "/api/v1/facets",
		Summary:     "Per-facet counts for the caller's library (exclude-self semantics)",
	}, func(ctx context.Context, in *facetsInput) (*facetsOutput, error) {
		return handleFacets(ctx, svc, in)
	})
}

// facetsInput is the bound query-string surface. Multi-value params
// (Camera, Lens, FacetTag, Tag) bind via huma's repeat-param convention,
// which requires the ,explode modifier — without it, repeated
// ?camera=A&camera=B collapses to the last value silently. Mirrors the
// existing /search route's Tag query field.
//
// Optional scalar params use sentinel zero-values rather than pointer
// types: huma does not support pointer-typed query/path/header params
// (it panics at registration), so each optional field uses its zero
// value to mean "not supplied". The handler converts non-zero values
// into the pointer-shaped facets.Filters fields. has_gps is a string
// with enum {"true","false"} so the tri-state (true / false / unset)
// round-trips cleanly.
type facetsInput struct {
	Camera        []string  `query:"camera,explode" doc:"repeatable; each value is a canonical \"make model\" bucket"`
	Lens          []string  `query:"lens,explode" doc:"repeatable; each value is a lens_model string"`
	FacetTag      []string  `query:"facet_tag,explode" doc:"repeatable; each value is a tag_key (any-of semantics for sidebar tag chips)"`
	Tag           []string  `query:"tag,explode" doc:"repeatable; each value is a tag label resolved server-side (and-of semantics for typed-chip strip)"`
	HasGPS        string    `query:"has_gps" enum:"true,false" doc:"true narrows to geotagged rows; false to non-geotagged; omit for no filter"`
	MediaType     string    `query:"media_type" enum:"photo,video" doc:"restrict to photos or videos"`
	DateAfter     time.Time `query:"date_after" doc:"include media whose timestamp is at or after this RFC3339 instant"`
	DateBefore    time.Time `query:"date_before" doc:"include media whose timestamp is before this RFC3339 instant"`
	Location      string    `query:"location" doc:"exact location label"`
	IncludeHidden bool      `query:"include_hidden" doc:"include hidden media; requires a hidden-unlock cookie"`
}

// facetValueDTO is the (value, count) wire shape used by Cameras,
// Lenses, and MediaTypes. Mirrors facets.ValueCount with explicit JSON
// tags so the spec is stable against domain renames.
type facetValueDTO struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// facetTagDTO is the (key, label, count) wire shape for the Tags
// facet. The frontend binds chips on key (the URL param) and renders
// label (the human-readable display).
type facetTagDTO struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// facetPlacesDTO is the with/without-GPS bucket pair. Mirrors
// facets.PlacesCount with explicit JSON tags.
type facetPlacesDTO struct {
	WithGPS    int `json:"with_gps"`
	WithoutGPS int `json:"without_gps"`
}

// facetsOutput wraps the response body so huma can document it. The
// Body field is populated by handleFacets.
type facetsOutput struct {
	Body struct {
		Cameras    []facetValueDTO `json:"cameras"`
		Lenses     []facetValueDTO `json:"lenses"`
		Tags       []facetTagDTO   `json:"tags"`
		Places     facetPlacesDTO  `json:"places"`
		MediaTypes []facetValueDTO `json:"media_types"`
	}
}

// handleFacets implements the facets endpoint logic split out from the
// huma.Register closure so it remains test-readable. The shape mirrors
// handleSearch: identity → claim plumbing → service call → DTO assembly.
//
// IncludeHidden + UnlockClaim flow through unchanged. The service does
// the validation in Aggregate and returns errs.ErrPermissionDenied when
// IncludeHidden=true but the claim is invalid — that maps to 403 via
// Translate, matching /search's hidden gate. Pre-validating here would
// re-create the bypass SF-5's commit closed (non-HTTP callers like the
// CLI must inherit the same gate).
func handleFacets(
	ctx context.Context,
	svc *facets.Service,
	in *facetsInput,
) (*facetsOutput, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
	}
	if svc == nil {
		return nil, huma.Error503ServiceUnavailable("facets service not configured")
	}
	caller := id.Principal.OwnersPrincipal()

	f := facets.Filters{
		Cameras:       in.Camera,
		Lenses:        in.Lens,
		AnyTagKeys:    in.FacetTag,
		TagKeys:       in.Tag,
		IncludeHidden: in.IncludeHidden,
	}
	// Optional scalars: convert zero-value sentinels into the
	// pointer-shaped facets.Filters fields so the service can
	// distinguish "not set" from "set to empty/false".
	if in.HasGPS != "" {
		v := in.HasGPS == "true"
		f.HasGPS = &v
	}
	if in.MediaType != "" {
		s := in.MediaType
		f.MediaType = &s
	}
	if in.Location != "" {
		s := in.Location
		f.LocationLabel = &s
	}
	if !in.DateAfter.IsZero() {
		t := in.DateAfter
		f.DateAfter = &t
	}
	if !in.DateBefore.IsZero() {
		t := in.DateBefore
		f.DateBefore = &t
	}
	// Surface the unlock claim only when it belongs to the caller —
	// mirrors handleSearch's claim-scoping, defense-in-depth against a
	// claim leaking across principals via a future middleware bug.
	if claim, hasClaim := hidden.UnlockClaimFromContext(ctx); hasClaim && claim.Principal == caller {
		c := claim
		f.UnlockClaim = &c
	}

	res, err := svc.Aggregate(ctx, caller, f)
	if err != nil {
		return nil, Translate(err)
	}

	out := &facetsOutput{}
	out.Body.Cameras = toValueDTOs(res.Cameras)
	out.Body.Lenses = toValueDTOs(res.Lenses)
	out.Body.Tags = toTagDTOs(res.Tags)
	out.Body.Places = facetPlacesDTO{
		WithGPS: res.Places.WithGPS, WithoutGPS: res.Places.WithoutGPS,
	}
	out.Body.MediaTypes = toValueDTOs(res.MediaTypes)
	return out, nil
}

// toValueDTOs converts a slice of facets.ValueCount into the wire
// shape. Length-preserving; nil-safe (a nil input yields a non-nil
// empty slice so the JSON shape is `"<facet>":[]` not `null`).
func toValueDTOs(in []facets.ValueCount) []facetValueDTO {
	out := make([]facetValueDTO, len(in))
	for i, v := range in {
		out[i] = facetValueDTO{Value: v.Value, Count: v.Count}
	}
	return out
}

// toTagDTOs converts a slice of facets.TagCount into the wire shape.
// Length-preserving; nil-safe.
func toTagDTOs(in []facets.TagCount) []facetTagDTO {
	out := make([]facetTagDTO, len(in))
	for i, t := range in {
		out[i] = facetTagDTO{Key: t.Key, Label: t.Label, Count: t.Count}
	}
	return out
}

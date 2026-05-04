// Package httpapi — the /api/v1/search* surface (search proper plus
// the two autocomplete endpoints) is split into this file so the
// search surface can evolve independently of the rest of the API.
// Routes are only registered when a *search.Service is wired into
// Deps; the OpenAPI dumper passes Deps{} so the search surface is
// absent from the dumped spec until the runtime wires it in.
package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/obs"
	"github.com/wesm/fotobank/internal/search/index"
	searchsvc "github.com/wesm/fotobank/internal/service/search"
)

// searchDefaultLimit matches the v1 plan's "60" default page size and
// is what the SPA assumes when it issues an unconfigured first page.
// Callers can override with ?limit=...; the upper bound is set by
// searchMaxLimit so a confused or hostile client can't request a
// huge page that consumes server memory and bandwidth.
const searchDefaultLimit = 60

// searchMaxLimit is the inclusive upper bound on ?limit. Above this
// value the route returns 400 BadRequest. The cap is enforced via
// huma's `maximum:` tag on searchInput so the request fails before
// any service / engine code runs; the runtime clamp below is a
// defense-in-depth fallback for the same boundary.
const searchMaxLimit = 200

// autocompleteDefaultLimit is the default page size for the two
// autocomplete endpoints when the client omits ?limit. Matches the
// SPA chip popover's default visible-row count (10).
const autocompleteDefaultLimit = 10

// autocompleteMaxLimit is the inclusive upper bound on the
// autocomplete endpoints' ?limit parameter. Smaller than searchMaxLimit
// because the chip popover never wants more than a couple dozen
// suggestions; the cap protects the DB from a forgetful caller.
const autocompleteMaxLimit = 50

// registerSearchRoutes mounts the search surface on api. svc==nil
// leaves every route unregistered (no handlers, no schemas) so the
// OpenAPI dumper can pass Deps{} unchanged. Production wiring supplies
// a fully-built *search.Service. m may be nil; the handler skips
// metric emits when so.
func registerSearchRoutes(api huma.API, svc *searchsvc.Service, m *obs.Metrics) {
	if svc == nil {
		return
	}
	huma.Register(api, huma.Operation{
		OperationID: "search",
		Method:      http.MethodGet,
		Path:        "/api/v1/search",
		Summary:     "Hybrid search across the caller's library",
	}, func(ctx context.Context, in *searchInput) (*searchOutput, error) {
		return handleSearch(ctx, svc, m, in)
	})
	registerSearchAutocompleteTags(api, svc)
	registerSearchAutocompleteLocations(api, svc)
}

// registerSearchAutocompleteTags mounts GET /api/v1/search/autocomplete/tags.
// Owner-scoped prefix-match autocomplete over media_tags.tag_label;
// owner / hidden gating is enforced by Service.AutocompleteTags.
func registerSearchAutocompleteTags(api huma.API, svc *searchsvc.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "search-autocomplete-tags",
		Method:      http.MethodGet,
		Path:        "/api/v1/search/autocomplete/tags",
		Summary:     "Prefix-match tag-label autocomplete for the caller's library",
	}, func(ctx context.Context, in *autocompleteTagsInput) (*autocompleteTagsOutput, error) {
		return handleAutocompleteTags(ctx, svc, in)
	})
}

// registerSearchAutocompleteLocations mounts
// GET /api/v1/search/autocomplete/locations. Substring-match autocomplete
// over media.location_label; owner / hidden gating is enforced by
// Service.AutocompleteLocations.
func registerSearchAutocompleteLocations(api huma.API, svc *searchsvc.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "search-autocomplete-locations",
		Method:      http.MethodGet,
		Path:        "/api/v1/search/autocomplete/locations",
		Summary:     "Substring-match location-label autocomplete for the caller's library",
	}, func(ctx context.Context, in *autocompleteLocationsInput) (*autocompleteLocationsOutput, error) {
		return handleAutocompleteLocations(ctx, svc, in)
	})
}

// handleAutocompleteTags identity-resolves the caller, surfaces any
// unlock claim in context, and delegates to Service.AutocompleteTags.
// Mirrors handleSearch's structure: identity → claim plumbing →
// service call → DTO assembly.
func handleAutocompleteTags(ctx context.Context, svc *searchsvc.Service, in *autocompleteTagsInput) (*autocompleteTagsOutput, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
	}
	caller := id.Principal.OwnersPrincipal()

	limit := in.Limit
	if limit <= 0 {
		limit = autocompleteDefaultLimit
	}
	// Defense-in-depth against a caller bypassing huma binding. The
	// `maximum:` tag on the input also surfaces 4xx for above-cap
	// values; this guard keeps the runtime contract honest.
	if limit > autocompleteMaxLimit {
		return nil, huma.Error400BadRequest(
			fmt.Sprintf("limit must be in 1..%d", autocompleteMaxLimit))
	}

	var claim *hidden.UnlockClaim
	if c, hasClaim := hidden.UnlockClaimFromContext(ctx); hasClaim && c.Principal == caller {
		claimCopy := c
		claim = &claimCopy
	}

	suggestions, err := svc.AutocompleteTags(ctx, caller, in.Prefix, limit, in.IncludeHidden, claim)
	if err != nil {
		return nil, Translate(err)
	}

	out := autocompleteTagsBody{Tags: make([]tagSuggestionDTO, 0, len(suggestions))}
	for _, s := range suggestions {
		out.Tags = append(out.Tags, tagSuggestionDTO{Key: s.Key, Label: s.Label, Count: s.Count})
	}
	return &autocompleteTagsOutput{Body: out}, nil
}

// handleAutocompleteLocations is the location-side twin of
// handleAutocompleteTags. Substring matching is the only behavioural
// difference; everything else (identity, hidden claim, limit clamp,
// translate) is identical.
func handleAutocompleteLocations(ctx context.Context, svc *searchsvc.Service, in *autocompleteLocationsInput) (*autocompleteLocationsOutput, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
	}
	caller := id.Principal.OwnersPrincipal()

	limit := in.Limit
	if limit <= 0 {
		limit = autocompleteDefaultLimit
	}
	if limit > autocompleteMaxLimit {
		return nil, huma.Error400BadRequest(
			fmt.Sprintf("limit must be in 1..%d", autocompleteMaxLimit))
	}

	var claim *hidden.UnlockClaim
	if c, hasClaim := hidden.UnlockClaimFromContext(ctx); hasClaim && c.Principal == caller {
		claimCopy := c
		claim = &claimCopy
	}

	suggestions, err := svc.AutocompleteLocations(ctx, caller, in.Substring, limit, in.IncludeHidden, claim)
	if err != nil {
		return nil, Translate(err)
	}

	out := autocompleteLocationsBody{Locations: make([]locationSuggestionDTO, 0, len(suggestions))}
	for _, s := range suggestions {
		out.Locations = append(out.Locations, locationSuggestionDTO{Label: s.Label, Count: s.Count})
	}
	return &autocompleteLocationsOutput{Body: out}, nil
}

// handleSearch implements the search endpoint logic split out from the
// huma.Register closure so it remains test-readable. The shape mirrors
// the AI handlers: identity → service call → translate → DTO assembly.
//
// m may be nil; the handler skips metric emits in that case so unit
// tests that pass Deps without a Metrics registry stay terse. When
// non-nil, the handler observes one SearchLatencySeconds bucket and
// one SearchRequestsTotal increment per *successful* engine response —
// failures (auth, translate) short-circuit before we know the engine
// mode and so don't fan out into per-mode counters.
func handleSearch(ctx context.Context, svc *searchsvc.Service, m *obs.Metrics, in *searchInput) (*searchOutput, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
	}
	caller := id.Principal.OwnersPrincipal()
	start := time.Now()

	req := searchsvc.Request{
		Query:         in.Q,
		Sort:          in.Sort,
		TagLabels:     in.Tag,
		IncludeHidden: in.IncludeHidden,
		Limit:         in.Limit,
		Cursor:        in.Cursor,
		Explain:       in.Explain,
		Cameras:       in.Camera,
		Lenses:        in.Lens,
		FacetTagKeys:  in.FacetTag,
	}
	// Optional time filters are only forwarded when the bound query
	// parameter parsed to a non-zero time. Huma stores zero-values for
	// missing time params, and treating zero as "no filter" keeps the
	// service path uniform whether the client omitted the param or
	// passed it explicitly empty.
	if !in.DateAfter.IsZero() {
		t := in.DateAfter
		req.DateAfter = &t
	}
	if !in.DateBefore.IsZero() {
		t := in.DateBefore
		req.DateBefore = &t
	}
	if in.Location != "" {
		s := in.Location
		req.LocationLabel = &s
	}
	if in.MediaType != "" {
		s := in.MediaType
		req.MediaType = &s
	}
	// HasGPS uses the same "string with enum" workaround as
	// /api/v1/facets and /api/v1/media: huma v2 panics on *bool query
	// params, so the wire surface uses literal "true"/"false" and the
	// handler converts to the pointer-shaped service field.
	switch in.HasGPS {
	case "true":
		t := true
		req.HasGPS = &t
	case "false":
		f := false
		req.HasGPS = &f
	}
	if in.Limit <= 0 {
		req.Limit = searchDefaultLimit
	}
	// Defense-in-depth: huma's `maximum:` tag on the bound input
	// already 400s requests above searchMaxLimit, but the runtime
	// check guards the (unlikely) case where the framework's tag
	// handling drifts or a future caller bypasses query binding.
	if req.Limit > searchMaxLimit {
		return nil, huma.Error400BadRequest(
			fmt.Sprintf("limit must be in 1..%d", searchMaxLimit))
	}
	// IncludeHidden=true requires a valid unlock claim in context. The
	// service performs the validity check via its HiddenChecker; we
	// merely surface the claim. A nil claim with IncludeHidden=true
	// short-circuits to ErrPermissionDenied inside the service.
	//
	// The same claim is forwarded to EmbeddingCompleteness below so
	// the completeness call validates independently — defense-in-depth
	// against a future caller that exercises only the completeness
	// path with includeHidden=true and no claim.
	var claimPtr *hidden.UnlockClaim
	if claim, hasClaim := hidden.UnlockClaimFromContext(ctx); hasClaim && claim.Principal == caller {
		c := claim
		claimPtr = &c
		req.UnlockClaim = &c
	}

	resp, err := svc.Search(ctx, caller, req)
	if err != nil {
		return nil, Translate(err)
	}
	if m != nil {
		// Stamp per-mode counters and the latency observation. The
		// engine's resp.EngineMode reflects the *taken* path (a
		// hybrid-eligible request that fell back to BM25 reports
		// "bm25_only"), which is what the dashboard needs to detect
		// silent degradation. effective_sort comes from resp, not
		// the raw input — the engine coerces relevance+empty-Q to
		// newest, and the metric should reflect what ran.
		mode := resp.EngineMode
		if mode == "" {
			// Defensive: an engine that forgot to stamp EngineMode
			// shouldn't strand the request without a label. "unknown"
			// surfaces the bug at the dashboard level.
			mode = "unknown"
		}
		m.SearchRequests(mode, resp.EffectiveSort).Inc()
		m.SearchLatency(mode).Update(time.Since(start).Seconds())
	}

	// EmbeddingCompleteness is a separate read so the result can render
	// the "X% indexed" pill without forcing every Search to JOIN against
	// media_embedding_ids. The service treats a missing active generation
	// as 0, so this never errors on a fresh library — just yields zero.
	completeness, err := svc.EmbeddingCompleteness(ctx, caller, in.IncludeHidden, claimPtr)
	if err != nil {
		return nil, Translate(err)
	}

	body := searchBody{
		Results:                   toSearchResultDTOs(resp.Hits, resp.Explain),
		HasMore:                   resp.HasMore,
		Total:                     resp.Total,
		EffectiveSort:             resp.EffectiveSort,
		EmbeddingCompleteness:     completeness,
		SemanticUnavailable:       resp.SemanticUnavailable,
		SemanticUnavailableReason: resp.SemanticUnavailableReason,
	}
	if resp.NextCursor != "" {
		c := resp.NextCursor
		body.NextCursor = &c
	}

	return &searchOutput{Body: body}, nil
}

// searchInput is the bound query-string surface for GET /api/v1/search.
// Slice-typed Tag uses `explode` so repeated `?tag=a&tag=b` parses into
// a slice; the default huma behaviour for a `[]string` query field
// would otherwise comma-split a single value, which doesn't compose
// with chip-based UIs that want each chip independently encoded.
//
// Camera, Lens, FacetTag, HasGPS are the sidebar-facet narrowers shared
// with /api/v1/facets and /api/v1/media (SF-17). HasGPS is `string`
// rather than `*bool` because huma v2 panics on pointer-typed query
// params; the handler converts the literal to *bool before populating
// the service request.
type searchInput struct {
	Q             string    `query:"q" doc:"free-text query (filter-only browse when empty)"`
	Sort          string    `query:"sort" enum:"relevance,newest,oldest" doc:"raw sort; engine may coerce (e.g. relevance + empty q → newest)"`
	DateAfter     time.Time `query:"date_after" doc:"include media whose timestamp is at or after this RFC3339 instant"`
	DateBefore    time.Time `query:"date_before" doc:"include media whose timestamp is before this RFC3339 instant"`
	Tag           []string  `query:"tag,explode" doc:"repeatable; each value is a tag label resolved server-side"`
	Location      string    `query:"location" doc:"exact location label"`
	MediaType     string    `query:"media_type" enum:"photo,video" doc:"restrict to photos or videos"`
	Limit         int       `query:"limit" minimum:"0" maximum:"200" doc:"page size; default 60, max 200"`
	Cursor        string    `query:"cursor" doc:"opaque next-page token from a previous response"`
	IncludeHidden bool      `query:"include_hidden" doc:"include hidden media; requires a hidden-unlock cookie"`
	Explain       bool      `query:"explain" doc:"return per-signal score components; gated on the AI Inspection setting"`
	Camera        []string  `query:"camera,explode" doc:"narrow to rows whose '<make> <model>' matches any value (OR-composed sidebar facet)"`
	Lens          []string  `query:"lens,explode" doc:"narrow to rows whose lens_model matches any value (OR-composed sidebar facet)"`
	FacetTag      []string  `query:"facet_tag,explode" doc:"narrow to rows that carry at least one tag matching any key (OR-composed sidebar facet, distinct from typed-chip 'tag')"`
	HasGPS        string    `query:"has_gps" enum:"true,false" doc:"true: only geotagged rows; false: only non-geotagged"`
}

// searchOutput wraps the response body so huma can document it. The
// Body field is populated by handleSearch.
type searchOutput struct {
	Body searchBody
}

// searchBody is the wire shape of the search response. Mirrors plan
// O1's contract: a results array, an optional next_cursor for paging,
// the engine's effective_sort (post-coercion), the embedding-completeness
// pill input, and the semantic-unavailable banner inputs.
type searchBody struct {
	Results                   []searchResultDTO `json:"results"`
	NextCursor                *string           `json:"next_cursor"`
	HasMore                   bool              `json:"has_more"`
	Total                     *int              `json:"total,omitempty"`
	EffectiveSort             string            `json:"effective_sort"`
	EmbeddingCompleteness     float64           `json:"embedding_completeness"`
	SemanticUnavailable       bool              `json:"semantic_unavailable"`
	SemanticUnavailableReason string            `json:"semantic_unavailable_reason"`
}

// searchResultDTO is the per-hit wire shape. Mirrors index.Hit but with
// explicit JSON tags so the spec is stable against domain renames.
// ScoreComponents is omitted when the underlying Hit didn't carry one
// (e.g. an early degradation path that didn't compute per-signal ranks).
type searchResultDTO struct {
	MediaID      string     `json:"media_id"`
	MediaType    string     `json:"media_type"`
	Timestamp    *time.Time `json:"timestamp"`
	ImportedAt   time.Time  `json:"imported_at"`
	Width        *int       `json:"width"`
	Height       *int       `json:"height"`
	ThumbVersion int        `json:"thumb_version"`
	// ThumbStatus is forwarded so the SPA's grid can render a shimmer
	// (pending/working) or a static placeholder (failed/no_preview)
	// instead of attempting an <img> that 404s. Without this column
	// the search route would treat every result as ready and the
	// retry-on-version-bump path stayed broken.
	ThumbStatus     string              `json:"thumb_status"`
	Score           float64             `json:"score,omitempty"`
	ScoreComponents *scoreComponentsDTO `json:"score_components,omitempty"`
}

// scoreComponentsDTO is the diagnostics-mode payload. RRF is the
// non-nullable fused score; the per-signal pieces (bm25, vector, ranks)
// are nullable because not every hit matched both signals. The wire
// shape uses `float64` for RRF (not a pointer) because every Hit that
// reaches this DTO has a valid RRF value — the index.Hit's *float64
// representation is internal and gets defaulted to 0 here.
type scoreComponentsDTO struct {
	RRF        float64  `json:"rrf"`
	BM25       *float64 `json:"bm25"`
	Vector     *float64 `json:"vector"`
	RankBM25   *int     `json:"rank_bm25"`
	RankVector *int     `json:"rank_vector"`
}

// toSearchResultDTOs converts the engine's per-hit slice into the wire
// shape. Length-preserving; nil-safe (empty input yields a non-nil
// empty slice so the JSON shape is always `"results":[]` not `null`).
//
// explain gates ScoreComponents emission: when false, the wire shape
// omits score_components even if the engine populated them. The
// service has already gated request.Explain on the caller's AI
// Inspection setting, and the engine forwards the gated value via
// resp.Explain — so the HTTP handler reads resp.Explain (not the
// raw request flag) to make this decision.
func toSearchResultDTOs(hits []index.Hit, explain bool) []searchResultDTO {
	out := make([]searchResultDTO, 0, len(hits))
	for _, h := range hits {
		out = append(out, toSearchResultDTO(h, explain))
	}
	return out
}

// toSearchResultDTO copies a single Hit into the DTO. The
// ScoreComponents conversion handles the nullable RRF on the engine
// side: index.Hit.ScoreComponents.RRF is *float64 (nil when the row
// only matched one signal); the wire DTO collapses that nil to 0,
// matching the field's non-nullable JSON shape. When explain is
// false the entire score_components substruct is omitted so the
// diagnostics-mode payload stays gated.
func toSearchResultDTO(h index.Hit, explain bool) searchResultDTO {
	dto := searchResultDTO{
		MediaID:      h.MediaID,
		MediaType:    h.MediaType,
		Timestamp:    h.Timestamp,
		ImportedAt:   h.ImportedAt,
		Width:        h.Width,
		Height:       h.Height,
		ThumbVersion: h.ThumbVersion,
		ThumbStatus:  h.ThumbStatus,
		Score:        h.Score,
	}
	if explain && h.ScoreComponents != nil {
		sc := &scoreComponentsDTO{
			BM25:       h.ScoreComponents.BM25,
			Vector:     h.ScoreComponents.Vector,
			RankBM25:   h.ScoreComponents.RankBM25,
			RankVector: h.ScoreComponents.RankVector,
		}
		if h.ScoreComponents.RRF != nil {
			sc.RRF = *h.ScoreComponents.RRF
		}
		dto.ScoreComponents = sc
	}
	return dto
}

// autocompleteTagsInput is the bound query-string surface for
// GET /api/v1/search/autocomplete/tags. Prefix is the user's typed
// fragment; the service appends the trailing % wildcard. include_hidden
// requires a hidden-unlock cookie (the service enforces; the route
// merely surfaces the claim).
type autocompleteTagsInput struct {
	Prefix        string `query:"prefix" doc:"prefix fragment; matched against tag_label with LIKE prefix%"`
	Limit         int    `query:"limit" minimum:"0" maximum:"50" doc:"page size; default 10, max 50"`
	IncludeHidden bool   `query:"include_hidden" doc:"include tags attached only to hidden media; requires a hidden-unlock cookie"`
}

// autocompleteTagsOutput wraps the response body so huma can document it.
type autocompleteTagsOutput struct {
	Body autocompleteTagsBody
}

// autocompleteTagsBody is the wire shape: a list of tag suggestions
// keyed by canonical tag_key, each with the display label and the
// number of caller-visible media that carry it. Length-preserving;
// nil-safe (empty input yields a non-nil empty slice so the JSON shape
// is `"tags":[]` not `null`).
type autocompleteTagsBody struct {
	Tags []tagSuggestionDTO `json:"tags"`
}

// tagSuggestionDTO is the per-row wire shape for the tags surface.
// Mirrors searchsvc.TagSuggestion with explicit JSON tags so the spec
// is stable against domain renames.
type tagSuggestionDTO struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// autocompleteLocationsInput is the bound query-string surface for
// GET /api/v1/search/autocomplete/locations. Substring is the user's
// typed fragment; the service wraps it in % markers on both sides for
// substring matching (locations are typically structured as
// "City, Region, Country" so users frequently type a country or
// region name rather than the full prefix).
type autocompleteLocationsInput struct {
	Substring     string `query:"substring" doc:"substring fragment; matched against location_label with LIKE %substring%"`
	Limit         int    `query:"limit" minimum:"0" maximum:"50" doc:"page size; default 10, max 50"`
	IncludeHidden bool   `query:"include_hidden" doc:"include locations attached only to hidden media; requires a hidden-unlock cookie"`
}

// autocompleteLocationsOutput wraps the response body so huma can
// document it.
type autocompleteLocationsOutput struct {
	Body autocompleteLocationsBody
}

// autocompleteLocationsBody is the wire shape: a list of location
// suggestions, each with the label and the number of caller-visible
// media that carry it. Length-preserving; nil-safe.
type autocompleteLocationsBody struct {
	Locations []locationSuggestionDTO `json:"locations"`
}

// locationSuggestionDTO is the per-row wire shape for the locations
// surface. Mirrors searchsvc.LocationSuggestion with explicit JSON tags.
// Locations don't carry a canonical key (the engine matches on the
// label directly) so the wire shape is (label, count) only.
type locationSuggestionDTO struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

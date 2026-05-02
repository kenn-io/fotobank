// Package httpapi — /api/v1/search route is split into this file so
// the search surface can evolve independently of the rest of the API.
// The route is only registered when a *search.Service is wired into
// Deps; the OpenAPI dumper passes Deps{} so the search surface is
// absent from the dumped spec until the runtime wires it in.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/search/index"
	searchsvc "github.com/wesm/fotobank/internal/service/search"
)

// searchDefaultLimit matches the v1 plan's "60" default page size and
// is what the SPA assumes when it issues an unconfigured first page.
// Callers can override with ?limit=...; the engine itself imposes no
// upper bound.
const searchDefaultLimit = 60

// registerSearchRoutes mounts GET /api/v1/search on api. svc==nil
// leaves the route unregistered (no handlers, no schemas) so the
// OpenAPI dumper can pass Deps{} unchanged. Production wiring supplies
// a fully-built *search.Service.
func registerSearchRoutes(api huma.API, svc *searchsvc.Service) {
	if svc == nil {
		return
	}
	huma.Register(api, huma.Operation{
		OperationID: "search",
		Method:      http.MethodGet,
		Path:        "/api/v1/search",
		Summary:     "Hybrid search across the caller's library",
	}, func(ctx context.Context, in *searchInput) (*searchOutput, error) {
		return handleSearch(ctx, svc, in)
	})
}

// handleSearch implements the search endpoint logic split out from the
// huma.Register closure so it remains test-readable. The shape mirrors
// the AI handlers: identity → service call → translate → DTO assembly.
func handleSearch(ctx context.Context, svc *searchsvc.Service, in *searchInput) (*searchOutput, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
	}
	caller := id.Principal.OwnersPrincipal()

	req := searchsvc.Request{
		Query:         in.Q,
		Sort:          in.Sort,
		TagLabels:     in.Tag,
		IncludeHidden: in.IncludeHidden,
		Limit:         in.Limit,
		Cursor:        in.Cursor,
		Explain:       in.Explain,
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
	if in.Limit <= 0 {
		req.Limit = searchDefaultLimit
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

	// EmbeddingCompleteness is a separate read so the result can render
	// the "X% indexed" pill without forcing every Search to JOIN against
	// media_embedding_ids. The service treats a missing active generation
	// as 0, so this never errors on a fresh library — just yields zero.
	completeness, err := svc.EmbeddingCompleteness(ctx, caller, in.IncludeHidden, claimPtr)
	if err != nil {
		return nil, Translate(err)
	}

	body := searchBody{
		Results:                   toSearchResultDTOs(resp.Hits),
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
type searchInput struct {
	Q             string    `query:"q" doc:"free-text query (filter-only browse when empty)"`
	Sort          string    `query:"sort" enum:"relevance,newest,oldest" doc:"raw sort; engine may coerce (e.g. relevance + empty q → newest)"`
	DateAfter     time.Time `query:"date_after" doc:"include media whose timestamp is at or after this RFC3339 instant"`
	DateBefore    time.Time `query:"date_before" doc:"include media whose timestamp is before this RFC3339 instant"`
	Tag           []string  `query:"tag,explode" doc:"repeatable; each value is a tag label resolved server-side"`
	Location      string    `query:"location" doc:"exact location label"`
	MediaType     string    `query:"media_type" enum:"photo,video" doc:"restrict to photos or videos"`
	Limit         int       `query:"limit" doc:"page size; default 60"`
	Cursor        string    `query:"cursor" doc:"opaque next-page token from a previous response"`
	IncludeHidden bool      `query:"include_hidden" doc:"include hidden media; requires a hidden-unlock cookie"`
	Explain       bool      `query:"explain" doc:"return per-signal score components; gated on the AI Inspection setting"`
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
	MediaID         string              `json:"media_id"`
	MediaType       string              `json:"media_type"`
	Timestamp       *time.Time          `json:"timestamp"`
	ImportedAt      time.Time           `json:"imported_at"`
	Width           *int                `json:"width"`
	Height          *int                `json:"height"`
	ThumbVersion    int                 `json:"thumb_version"`
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
func toSearchResultDTOs(hits []index.Hit) []searchResultDTO {
	out := make([]searchResultDTO, 0, len(hits))
	for _, h := range hits {
		out = append(out, toSearchResultDTO(h))
	}
	return out
}

// toSearchResultDTO copies a single Hit into the DTO. The
// ScoreComponents conversion handles the nullable RRF on the engine
// side: index.Hit.ScoreComponents.RRF is *float64 (nil when the row
// only matched one signal); the wire DTO collapses that nil to 0,
// matching the field's non-nullable JSON shape.
func toSearchResultDTO(h index.Hit) searchResultDTO {
	dto := searchResultDTO{
		MediaID:      h.MediaID,
		MediaType:    h.MediaType,
		Timestamp:    h.Timestamp,
		ImportedAt:   h.ImportedAt,
		Width:        h.Width,
		Height:       h.Height,
		ThumbVersion: h.ThumbVersion,
		Score:        h.Score,
	}
	if h.ScoreComponents != nil {
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

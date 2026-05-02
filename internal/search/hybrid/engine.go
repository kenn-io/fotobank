package hybrid

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/search"
	"github.com/wesm/fotobank/internal/search/index"
)

// Engine is the search v1 routing layer that sits between the service
// boundary and the index Backend. Search picks the right Backend
// method (FusedSearch / BM25Only / FilterOnly) based on the request
// shape and the availability of a query-time embedding signal, then
// degrades gracefully when the active generation is missing or the
// query embedding call fails.
//
// The engine is stateless apart from its dependencies; one instance
// per process is fine.
type Engine struct {
	backend     index.Backend
	embedClient embedding.ClientIface
	gens        *embedding.Generations
	cfg         search.Config
}

// NewEngine wires an Engine over the supplied dependencies. b drives
// the SQL side, c is the embeddings HTTP client (needed only for
// query-time text encoding), g resolves the active generation row
// (carries dim, model, vec table name), and cfg supplies KPerSignal /
// RRFK.
//
// All four are required in production; tests may pass nil for c or g
// to exercise the no-active-generation / embed-failure degradation
// paths via the routing logic in Search.
func NewEngine(b index.Backend, c embedding.ClientIface, g *embedding.Generations, cfg search.Config) *Engine {
	return &Engine{backend: b, embedClient: c, gens: g, cfg: cfg}
}

// Request is the engine-facing request shape. Sort is the *raw* user
// input ("relevance" | "newest" | "oldest" | empty); the engine
// coerces it to an effective sort before invoking the backend. Filter
// carries the structured query already parsed by the service layer
// (M1's Input). Cursor is opaque — empty for the first page.
type Request struct {
	Owner         owners.Principal
	Query         string
	Sort          string
	Filter        Input
	IncludeHidden bool
	Limit         int
	Cursor        string
	Explain       bool
}

// Response is the engine-facing response shape. EffectiveSort is the
// post-coercion sort label (e.g. "relevance" → "newest" when Q is
// empty). EmbeddingCompleteness is the fraction of the active
// generation that has been embedded — populated by N2; v1 leaves it
// at zero. SemanticUnavailable / SemanticUnavailableReason surface
// degradation to the UI banner so the user knows the result set is
// BM25-only or filter-only.
//
// Explain mirrors the request's gated Explain flag — the service
// has already gated it on the caller's AI Inspection setting, so
// the engine just passes the value through. The HTTP handler uses
// resp.Explain to decide whether to emit per-signal score
// components on the wire; consumers that read engine output
// directly (CLI, internal callers) can use it for the same
// gating decision without re-deriving it from the request.
type Response struct {
	Hits                      []index.Hit
	NextCursor                string
	HasMore                   bool
	Total                     *int
	EffectiveSort             string
	EmbeddingCompleteness     float64
	SemanticUnavailable       bool
	SemanticUnavailableReason string
	Explain                   bool
	// EngineMode is the routing decision the engine ended up taking
	// for this request: one of {"hybrid", "bm25_only", "filter_only"}.
	// Surfaced so observability callers (HTTP handler's metric emit)
	// can label by the degraded path, not the raw request shape — a
	// hybrid-eligible request that fell back to BM25 reports
	// mode="bm25_only".
	EngineMode string
}

// engine-mode labels populated into Response.EffectiveSort and the
// cursor's NormalizedReq.EngineMode. Kept as untyped string constants
// so they can flow into both fields without conversion gymnastics.
const (
	engineModeHybrid     = "hybrid"
	engineModeBM25Only   = "bm25_only"
	engineModeFilterOnly = "filter_only"
)

// Search is the routing fan-out. The pseudocode follows the plan's
// step list verbatim; the comments below name the branches by their
// engine-mode label so the corresponding Response.SemanticUnavailable
// flag is easy to trace.
//
// Branches (from top to bottom):
//
//  1. Q is empty → FilterOnly with Sort coerced to date.
//  2. Q non-empty + effective Sort=relevance:
//     a. Build MATCH expression. If every token dropped → recurse as
//     FilterOnly (the user typed pure punctuation; treat as an empty
//     query rather than 400-ing).
//     b. Resolve active generation. nil → BM25Only with reason
//     "no_active_generation".
//     c. EmbedTexts the query. Failure → BM25Only with reason
//     "query_embedding_failed".
//     d. Otherwise FusedSearch with the encoded vector.
//  3. Q non-empty + effective Sort in {newest, oldest}: same Backend
//     method as (2) but the per-mode SELECT applies the date sort.
//     The "over-fetch then re-sort" optimization the plan calls out
//     is deferred — v1 hands the date sort straight through to the
//     backend's existing ORDER BY.
//
// Cursor encoding for v1 is intentionally simple: when len(hits) ==
// Limit we emit a cursor with the request hash so the next page
// round-trip will validate, but K1/K2/ID are populated from the last
// hit as a placeholder for the future page-skip math. HasMore is
// driven off the same len(hits) == Limit signal.
func (e *Engine) Search(ctx context.Context, req Request) (Response, error) {
	// Step 1: coerce Sort to an effective value. "relevance" with no
	// query has no signal to rank by, so degrade to "newest".
	effSort := req.Sort
	if req.Query == "" && effSort == string(index.SortRelevance) {
		effSort = string(index.SortNewest)
	}
	if effSort == "" {
		// Empty raw sort with non-empty Q → relevance is the sensible
		// default; with empty Q → newest. Both paths converge below.
		if req.Query == "" {
			effSort = string(index.SortNewest)
		} else {
			effSort = string(index.SortRelevance)
		}
	}

	// Step 2: probe the active generation. A nil generation flips the
	// semantic-unavailable flag for every Q-non-empty branch below;
	// the FilterOnly branch ignores the flag because it never consults
	// embeddings.
	var activeGen *embedding.Row
	var semanticUnavailable bool
	var semanticUnavailableReason string
	if e.gens != nil {
		row, err := e.gens.FindActive(ctx)
		if err != nil {
			// FindActive only errors on a hard DB failure; treat as
			// no-active-gen and degrade. Logging the cause keeps the
			// failure observable without forcing the request to fail.
			slog.Default().Warn("search engine: find active generation failed",
				"error", err)
		} else {
			activeGen = row
		}
	}
	if activeGen == nil {
		semanticUnavailable = true
		semanticUnavailableReason = "no_active_generation"
	}

	// Filter is M1's structured Input with the engine's per-request
	// flags stamped in. The service layer (N1) is responsible for
	// rejecting IncludeHidden=true without an unlock claim; the engine
	// only forwards the already-validated flag.
	filter := req.Filter.WithOwner(req.Owner).WithHidden(req.IncludeHidden)
	cteSQL, cteArgs := Resolve(filter)
	filterCTE := index.FilterCTE{SQL: cteSQL, Args: cteArgs}

	// Step 3: route. Empty-query branch and "all tokens dropped"
	// branch share the same FilterOnly call — extracted into a helper
	// so the recursion in the relevance branch is honest.
	//
	// FilterOnly never consults the semantic signal, so the
	// semantic-unavailable flag must be cleared in this branch even if
	// no active generation was resolved above. Otherwise an empty-Q
	// page on a fresh library would render the "semantic unavailable"
	// banner — which is misleading because the request never asked for
	// a semantic signal in the first place.
	if req.Query == "" {
		if err := validateCursor(req, effSort, engineModeFilterOnly); err != nil {
			return Response{}, err
		}
		hits, err := e.runFilterOnly(ctx, filterCTE, effSort, req.Limit)
		if err != nil {
			return Response{}, err
		}
		return e.buildResponse(req, hits, effSort, engineModeFilterOnly,
			false, ""), nil
	}

	// Q is non-empty. Build the MATCH expression once; if it collapses
	// to empty (the user's tokens were all stripped), recurse as
	// FilterOnly so the request still returns the owner's media in
	// date order rather than 400-ing on pure punctuation.
	matchExpr, ok := BuildMatchExpr(req.Query)
	if !ok {
		// Empty match → no lexical signal. Coerce sort to a date sort
		// (relevance is meaningless without a query) and serve from
		// the filter CTE. As with the empty-Q branch, the
		// semantic-unavailable flag is cleared here — pure-punctuation
		// queries collapse to a filter-only request and the user
		// never asked for semantic ranking.
		filterSort := effSort
		if filterSort == string(index.SortRelevance) {
			filterSort = string(index.SortNewest)
		}
		if err := validateCursor(req, filterSort, engineModeFilterOnly); err != nil {
			return Response{}, err
		}
		hits, err := e.runFilterOnly(ctx, filterCTE, filterSort, req.Limit)
		if err != nil {
			return Response{}, err
		}
		return e.buildResponse(req, hits, filterSort, engineModeFilterOnly,
			false, ""), nil
	}

	// Decide between FusedSearch and BM25Only based on whether the
	// semantic signal is available. activeGen nil already flipped the
	// flag above; an EmbedTexts error flips it here.
	//
	// The shared ClientIface only declares EmbedImages because the
	// worker side never needs EmbedTexts. The engine type-asserts on
	// a local extension (textEmbedder) so tests can substitute a fake
	// that opts out of EmbedTexts to exercise the degradation path.
	// In production *embedding.Client satisfies textEmbedder, so the
	// non-test path always finds the assertion succeeds.
	var queryVec []float32
	if !semanticUnavailable && e.embedClient != nil && activeGen != nil {
		texter, isTexter := e.embedClient.(textEmbedder)
		switch {
		case !isTexter:
			semanticUnavailable = true
			semanticUnavailableReason = "query_embedding_failed"
		default:
			tvecs, terr := texter.EmbedTexts(ctx, activeGen.ModelID, activeGen.Dimension, []string{req.Query})
			if terr != nil || len(tvecs) == 0 || len(tvecs[0]) == 0 {
				semanticUnavailable = true
				semanticUnavailableReason = "query_embedding_failed"
				if terr != nil {
					slog.Default().Warn("search engine: query embedding failed",
						"error", terr)
				}
			} else {
				queryVec = tvecs[0]
			}
		}
	}

	in := index.SearchInput{
		Query:         matchExpr,
		QueryVector:   queryVec,
		Owner:         req.Owner,
		IncludeHidden: req.IncludeHidden,
		Filter:        filterCTE,
		Sort:          index.SortFromString(effSort),
		KPerSignal:    e.cfg.KPerSignal,
		RRFK:          e.cfg.RRFK,
		Limit:         req.Limit,
		// Gen is populated per-request from FindActive above; the
		// SQLiteVecBackend in production is constructed with a
		// zero-value Row, so this is the only path through which
		// FusedSearch learns which generation's vec0 table to read.
		// activeGen may be nil here even though semanticUnavailable
		// is false (e.g. an embed call returned vectors but the gen
		// row is nil — guarded by activeGen-nil checks above), so
		// guard the assignment to avoid stamping an empty Row.
		Gen: activeGen,
	}
	// TODO(R1): emit obs.Metrics.SearchPoolSaturated.Inc() when the
	// per-signal candidate pool was filled to KPerSignal. The current
	// backend SQL caps both the BM25 and ANN CTEs at KPerSignal but
	// does not surface the pre-cap count to the engine — the post-fusion
	// hit set is bounded by Limit, not KPerSignal, so len(hits) ==
	// KPerSignal is not a reliable saturation signal. Plumb a per-signal
	// `count_pre_cap` value out of FusedSearch / BM25Only (e.g. via a
	// supplementary CTE that COUNT(*)s the raw candidates) and emit
	// the metric when count_pre_cap >= KPerSignal here.

	if !semanticUnavailable && len(queryVec) > 0 {
		if err := validateCursor(req, effSort, engineModeHybrid); err != nil {
			return Response{}, err
		}
		hits, err := e.backend.FusedSearch(ctx, in)
		if err != nil {
			return Response{}, fmt.Errorf("fused search: %w", err)
		}
		return e.buildResponse(req, hits, effSort, engineModeHybrid,
			semanticUnavailable, semanticUnavailableReason), nil
	}

	// Semantic unavailable for any reason → BM25Only.
	if err := validateCursor(req, effSort, engineModeBM25Only); err != nil {
		return Response{}, err
	}
	hits, err := e.backend.BM25Only(ctx, in)
	if err != nil {
		return Response{}, fmt.Errorf("bm25 search: %w", err)
	}
	return e.buildResponse(req, hits, effSort, engineModeBM25Only,
		semanticUnavailable, semanticUnavailableReason), nil
}

// validateCursor enforces the round-trip contract: when req.Cursor is
// non-empty, it must decode and its ReqHash must match the freshly-
// computed hash of the current request shape. A mismatch most likely
// means the client changed Q / Sort / Filter between page 1 and page 2;
// honouring the cursor anyway would page through a result set that no
// longer matches the visible query, so we surface the violation as
// errs.ErrInvalidArgument (route layer maps this to 400).
//
// v1 does not yet consume the decoded Cursor.K1 / K2 / ID for page-skip
// math — the engine's per-mode SELECT still returns the first N rows
// regardless. The validation here is forward-compatible so a tampered
// cursor or a query-shift between pages cannot leak rows the next-page
// math will eventually paginate.
func validateCursor(req Request, effSort, mode string) error {
	if req.Cursor == "" {
		return nil
	}
	expected := NormalizedHash(NormalizedReq{
		Q:             req.Query,
		Sort:          effSort,
		IncludeHidden: req.IncludeHidden,
		EngineMode:    mode,
		Filter:        flattenFilter(req.Filter),
	})
	if _, err := DecodeCursorAndCheck(req.Cursor, expected); err != nil {
		return fmt.Errorf("validate cursor: %w: %w", errs.ErrInvalidArgument, err)
	}
	return nil
}

// textEmbedder is the optional capability the engine extracts from
// embedding.ClientIface for query-time text encoding. The shared
// ClientIface (defined in the embedding package) only declares
// EmbedImages because the worker side never needs EmbedTexts. Adding
// EmbedTexts to the embedding-package interface would force every
// fake worker test to implement an unused method, so the engine
// type-asserts on a local extension instead.
type textEmbedder interface {
	EmbedTexts(ctx context.Context, model string, dimension int, texts []string) ([][]float32, error)
}

// runFilterOnly is a tiny helper so the empty-query and
// all-tokens-dropped branches share one shape. The Sort coercion is
// the caller's responsibility — by this point effSort is one of
// {newest, oldest, relevance}, and FilterOnly treats relevance as
// newest internally.
func (e *Engine) runFilterOnly(ctx context.Context, cte index.FilterCTE, effSort string, limit int) ([]index.Hit, error) {
	in := index.SearchInput{
		Filter: cte,
		Sort:   index.SortFromString(effSort),
		Limit:  limit,
	}
	hits, err := e.backend.FilterOnly(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("filter only: %w", err)
	}
	return hits, nil
}

// buildResponse stamps the per-request flags onto a Response and
// produces the next-page cursor when len(hits) == Limit. The cursor
// fields K1/K2/ID are placeholder values (last hit's score, timestamp,
// id) — the page-skip math that consumes them lands in a follow-up;
// for v1 the cursor exists so the client can verify ReqHash on the
// next page and the engine can wire HasMore off len(hits) == Limit.
//
// Explain is forwarded from the request unchanged — the service has
// already gated it on the caller's AI Inspection setting before
// handing the engine a Request. Consumers (HTTP handler, CLI) read
// resp.Explain to decide whether to emit per-signal score components.
func (e *Engine) buildResponse(req Request, hits []index.Hit, effSort, mode string,
	semanticUnavailable bool, semanticReason string) Response {

	resp := Response{
		Hits:                      hits,
		EffectiveSort:             effSort,
		SemanticUnavailable:       semanticUnavailable,
		SemanticUnavailableReason: semanticReason,
		Explain:                   req.Explain,
		EngineMode:                mode,
	}

	if req.Limit > 0 && len(hits) == req.Limit {
		// Hash the *effective* request so a sort-coercion (e.g. raw
		// "relevance" → effective "newest" when Q is empty) does not
		// invalidate the cursor on the next page.
		hash := NormalizedHash(NormalizedReq{
			Q:             req.Query,
			Sort:          effSort,
			IncludeHidden: req.IncludeHidden,
			EngineMode:    mode,
			Filter:        flattenFilter(req.Filter),
		})
		last := hits[len(hits)-1]
		c := Cursor{
			ReqHash: hash,
			K1:      last.Score,
			ID:      last.MediaID,
		}
		if last.Timestamp != nil {
			c.K2 = last.Timestamp.Unix()
		} else {
			c.K2 = last.ImportedAt.Unix()
		}
		resp.NextCursor = EncodeCursor(c)
		resp.HasMore = true
	}

	return resp
}

// flattenFilter projects a structured Input into the flat
// string→string map NormalizedHash hashes over. Multi-valued keys
// (TagKeys) join their values with "," so a re-order of the slice
// does not change the hash. Owner is intentionally omitted — the
// service layer scopes by Owner before the cursor is observed, and a
// shifted Owner is a different request that should never see another
// owner's cursor anyway.
func flattenFilter(in Input) map[string]string {
	out := map[string]string{}
	if in.DateAfter != nil {
		out["date_after"] = in.DateAfter.UTC().Format("2006-01-02T15:04:05Z")
	}
	if in.DateBefore != nil {
		out["date_before"] = in.DateBefore.UTC().Format("2006-01-02T15:04:05Z")
	}
	if len(in.TagKeys) > 0 {
		// Sort the tag keys before joining so the cursor hash is
		// independent of the slice order the resolver / caller emitted.
		// Without this, two requests that differ only in TagKeys
		// permutation would produce different hashes and a cursor
		// minted on page 1 would 400 on page 2 simply because the
		// resolver re-emitted the same labels in a different order.
		keys := append([]string(nil), in.TagKeys...)
		sort.Strings(keys)
		var joined strings.Builder
		for i, k := range keys {
			if i > 0 {
				joined.WriteString(",")
			}
			joined.WriteString(k)
		}
		out["tags"] = joined.String()
	}
	if in.LocationLabel != nil {
		out["location"] = *in.LocationLabel
	}
	if in.MediaType != nil {
		out["media_type"] = *in.MediaType
	}
	return out
}

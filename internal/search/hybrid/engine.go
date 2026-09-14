package hybrid

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/search"
	"go.kenn.io/fotobank/internal/search/index"
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
	embedClient func(context.Context) embedding.ClientIface
	gens        *embedding.Generations
	cfg         search.Config
}

// NewEngine wires an Engine over the supplied dependencies. b drives
// the SQL side, c resolves a client from current settings once per text query,
// and g resolves the active generation row
// (carries dim, model, vec table name), and cfg supplies KPerSignal /
// RRFK.
//
// A nil resolver or returned client disables semantic search. A query already
// in flight finishes with its captured settings; later queries see updates.
func NewEngine(b index.Backend, c func(context.Context) embedding.ClientIface, g *embedding.Generations, cfg search.Config) *Engine {
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

// engine-mode labels populated into Response.EngineMode and the
// cursor's NormalizedReq.EngineMode. Kept as untyped string constants
// so they can flow into both fields without conversion gymnastics.
const (
	engineModeHybrid     = "hybrid"
	engineModeBM25Only   = "bm25_only"
	engineModeFilterOnly = "filter_only"
)

// Search returns one live page. The cursor binds an offset to the effective
// query, owner, filters, ranking settings, and (for hybrid search) generation
// and query vector. A changed vector requires restarting pagination.
func (e *Engine) Search(ctx context.Context, req Request) (Response, error) {
	if req.Limit <= 0 || req.Limit == math.MaxInt {
		return Response{}, fmt.Errorf("positive bounded page size required: %w", errs.ErrInvalidArgument)
	}
	effSort := req.Sort
	if effSort == "" {
		effSort = string(index.SortRelevance)
	}
	matchExpr, hasText := BuildMatchExpr(req.Query)
	mode := engineModeFilterOnly
	if !hasText && effSort == string(index.SortRelevance) {
		effSort = string(index.SortNewest)
	}

	var activeGen *embedding.Row
	var queryVec []float32
	semanticReason := ""
	if hasText {
		mode = engineModeBM25Only
		var client embedding.ClientIface
		if e.embedClient != nil {
			client = e.embedClient(ctx)
		}
		switch client {
		case nil:
			semanticReason = "embeddings_disabled"
		default:
			if e.gens != nil {
				row, err := e.gens.FindActive(ctx)
				if err != nil {
					slog.Default().Warn("search engine: find active generation failed", "error", err)
				} else {
					activeGen = row
				}
			}
			if activeGen == nil {
				semanticReason = "no_active_generation"
			} else if texter, ok := client.(textEmbedder); ok {
				vectors, err := texter.EmbedTexts(ctx, activeGen.ModelID, activeGen.Dimension, []string{req.Query})
				if err != nil || len(vectors) == 0 || len(vectors[0]) == 0 {
					semanticReason = "query_embedding_failed"
					if err != nil {
						slog.Default().Warn("search engine: query embedding failed", "error", err)
					}
				} else {
					queryVec = vectors[0]
					mode = engineModeHybrid
				}
			} else {
				semanticReason = "query_embedding_failed"
			}
		}
	}

	binding := NormalizedReq{
		Q: req.Query, Sort: effSort, IncludeHidden: req.IncludeHidden,
		EngineMode: mode, Filter: flattenFilter(req.Filter), Owner: req.Owner,
		KPerSignal: e.cfg.KPerSignal, RRFK: e.cfg.RRFK,
	}
	if mode == engineModeHybrid && activeGen != nil {
		binding.GenerationID = activeGen.ID
		binding.QueryVector = queryVec
	}
	hash := NormalizedHash(binding)
	offset := 0
	if req.Cursor != "" {
		cursor, err := DecodeCursorAndCheck(req.Cursor, hash)
		if err != nil {
			return Response{}, fmt.Errorf("validate cursor: %w: %w", errs.ErrInvalidArgument, err)
		}
		if cursor.Offset <= 0 || cursor.Offset > math.MaxInt-req.Limit-1 {
			return Response{}, fmt.Errorf("invalid cursor offset: %w", errs.ErrInvalidArgument)
		}
		offset = cursor.Offset
	}

	filter := req.Filter.WithOwner(req.Owner).WithHidden(req.IncludeHidden)
	sql, args := Resolve(filter)
	in := index.SearchInput{
		Query: matchExpr, QueryVector: queryVec, Owner: req.Owner,
		IncludeHidden: req.IncludeHidden, Filter: index.FilterCTE{SQL: sql, Args: args},
		Sort: index.SortFromString(effSort), KPerSignal: e.cfg.KPerSignal, RRFK: e.cfg.RRFK,
		Limit: req.Limit + 1, Offset: offset, Gen: activeGen,
	}
	var hits []index.Hit
	var err error
	switch mode {
	case engineModeFilterOnly:
		hits, err = e.backend.FilterOnly(ctx, in)
	case engineModeBM25Only:
		hits, err = e.backend.BM25Only(ctx, in)
	case engineModeHybrid:
		hits, err = e.backend.FusedSearch(ctx, in)
	}
	if err != nil {
		return Response{}, fmt.Errorf("%s search: %w", mode, err)
	}
	resp := Response{
		Hits: hits, EffectiveSort: effSort, EngineMode: mode, Explain: req.Explain,
		SemanticUnavailable: semanticReason != "", SemanticUnavailableReason: semanticReason,
	}
	if len(hits) > req.Limit {
		resp.Hits = hits[:req.Limit]
		resp.HasMore = true
		resp.NextCursor = EncodeCursor(Cursor{ReqHash: hash, Offset: offset + req.Limit})
	}
	return resp, nil
}

// textEmbedder is the query-time capability of the shared embedding client.
type textEmbedder interface {
	EmbedTexts(ctx context.Context, model string, dimension int, texts []string) ([][]float32, error)
}

// flattenFilter projects a structured Input into the flat
// string→string map NormalizedHash hashes over. Sorted JSON arrays preserve
// multi-value boundaries; owner is bound separately in NormalizedReq.
func flattenFilter(in Input) map[string]string {
	out := map[string]string{}
	if in.DateAfter != nil {
		out["date_after"] = in.DateAfter.UTC().Format(time.RFC3339Nano)
	}
	if in.DateBefore != nil {
		out["date_before"] = in.DateBefore.UTC().Format(time.RFC3339Nano)
	}
	if v := joinSorted(in.TagKeys); v != "" {
		out["tags"] = v
	}
	if v := joinSorted(in.Cameras); v != "" {
		out["cameras"] = v
	}
	if v := joinSorted(in.Lenses); v != "" {
		out["lenses"] = v
	}
	if v := joinSorted(in.AnyTagKeys); v != "" {
		out["any_tags"] = v
	}
	if in.LocationLabel != nil {
		out["location"] = *in.LocationLabel
	}
	if in.MediaType != nil {
		out["media_type"] = *in.MediaType
	}
	if in.HasGPS != nil {
		if *in.HasGPS {
			out["has_gps"] = "true"
		} else {
			out["has_gps"] = "false"
		}
	}
	return out
}

// joinSorted returns the input slice's values as a sorted JSON array,
// or "" for an empty slice. Used by flattenFilter so two
// requests that differ only in slice permutation produce the same
// cursor hash — without this, a resolver / caller that emits the
// same set in a different order would 400 on page 2.
func joinSorted(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	keys := append([]string(nil), vs...)
	sort.Strings(keys)
	encoded, _ := json.Marshal(keys)
	return string(encoded)
}

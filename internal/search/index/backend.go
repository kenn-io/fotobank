package index

import (
	"context"
	"time"

	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/owners"
)

// Backend is the read-side abstraction the hybrid Engine sits on top
// of. The engine builds a SearchInput, picks the right Backend method
// for the request shape, and returns the hits to the service layer.
//
// Three call shapes:
//
//   - FusedSearch  — both lexical and semantic signals are present;
//     BM25 + ANN are intersected with the filter and fused via RRF.
//   - BM25Only     — query text but no semantic signal (e.g. no active
//     embedding generation, or feature-flagged off). FTS5 ranks alone.
//   - FilterOnly   — neither query text nor query vector; the filter
//     CTE drives the result set, sorted by SearchInput.Sort.
//
// All three return rows in best-first order with at most SearchInput.Limit
// elements, starting at Offset in the ordered result set.
type Backend interface {
	FusedSearch(ctx context.Context, in SearchInput) ([]Hit, error)
	BM25Only(ctx context.Context, in SearchInput) ([]Hit, error)
	FilterOnly(ctx context.Context, in SearchInput) ([]Hit, error)
}

// SearchInput is the per-request bundle of every signal a Backend
// method may consume. Fields irrelevant to the chosen mode are ignored
// (FilterOnly does not look at Query / QueryVector / KPerSignal /
// RRFK). Every call requires an explicit owner-scoped Filter.SQL.
type SearchInput struct {
	// Query is a tokenized + escaped FTS5 MATCH expression. For L1 the
	// caller is the test helper; M2 produces the shape from raw user
	// input.
	Query string
	// QueryVector is the dense embedding the caller is searching with.
	// Empty when the engine routes BM25Only or FilterOnly.
	QueryVector []float32
	// Owner is the principal whose media may appear in results. The
	// filter CTE is responsible for owner scoping; this field is held
	// for callers that want to inspect the resolved request (e.g. for
	// metrics or logging) without re-parsing Filter.SQL.
	Owner owners.Principal
	// IncludeHidden mirrors the request flag; the filter resolver in M1
	// is responsible for translating it into a `hidden_at IS NULL`
	// predicate (or omitting it) inside Filter.SQL.
	IncludeHidden bool
	// Filter carries the pre-resolved owner / media-type / date / tag /
	// location / hidden CTE body and its bind args. The Backend treats
	// it as opaque SQL.
	Filter FilterCTE
	// Sort orders the selected candidates by relevance or capture date.
	// FilterOnly treats relevance as newest.
	Sort Sort
	// KPerSignal caps the per-signal candidate pool for both the BM25
	// and ANN CTEs. Higher values trade latency for recall.
	KPerSignal int
	// RRFK is the smoothing constant in the RRF formula
	// 1.0 / (RRFK + rank). Plan v1 default is 60.
	RRFK int
	// Limit caps the final result set size returned to the caller.
	Limit int
	// Offset skips rows after ranking and visibility filtering.
	// ponytail: deep pages scan skipped rows; use keysets if that cost matters.
	Offset int
	// Gen is the active embedding generation resolved for THIS request.
	// FusedSearch uses it to look up the per-generation vec0 table; when
	// nil, the backend falls back to its construction-time generation
	// (the legacy behaviour). The engine populates Gen per-request from
	// embedding.Generations.FindActive so a promote/retire that lands
	// between server boot and the request takes effect immediately —
	// the construction-time row goes stale otherwise.
	//
	// Required for FusedSearch when the construction-time generation is
	// the zero value; BM25Only and FilterOnly never consult it.
	Gen *embedding.Row
}

// Hit is the per-result row the Backend returns. Width / Height /
// Timestamp are *int / *time.Time because their underlying media
// columns are nullable; ImportedAt is non-nullable, so a value type is
// fine.
type Hit struct {
	MediaID      string
	MediaType    string
	Timestamp    *time.Time
	ImportedAt   time.Time
	Width        *int
	Height       *int
	ThumbVersion int
	// ThumbStatus mirrors media.thumb_status. Forwarded to the wire DTO
	// so the SPA's grid can branch between "render the thumb",
	// "shimmer (still working)", and "placeholder (no preview)" — the
	// /thumb endpoint 404s for any state other than "ready", and
	// without this column the frontend would attempt and fail.
	ThumbStatus     string
	Score           float64          // RRF or BM25 depending on engine mode
	ScoreComponents *ScoreComponents // populated by every Backend mode
}

// ScoreComponents carries the per-signal pieces that fed into Score.
// All fields are pointers so a missing signal (e.g. a row that only
// matched the BM25 CTE has no Vector / RankVector) is observably nil
// rather than a zero numeric value the caller could mistake for a
// real rank.
type ScoreComponents struct {
	RRF        *float64
	BM25       *float64
	Vector     *float64
	RankBM25   *int
	RankVector *int
}

// Sort selects relevance or capture-date ordering in all three modes.
type Sort string

const (
	SortRelevance Sort = "relevance"
	SortNewest    Sort = "newest"
	SortOldest    Sort = "oldest"
)

// SortFromString maps the engine's effective-sort string onto the
// strongly-typed Sort. Unknown values fall through to SortNewest —
// the safest default for FilterOnly callers and the documented fallback
// the SQLiteVecBackend already implements in its switch.
func SortFromString(s string) Sort {
	switch Sort(s) {
	case SortRelevance:
		return SortRelevance
	case SortOldest:
		return SortOldest
	case SortNewest:
		return SortNewest
	default:
		return SortNewest
	}
}

// FilterCTE is the SQL body and bind args that pre-resolve the
// per-request media subset. The body must be a SELECT producing the
// columns (id, timestamp, imported_at) — the Backend's CTEs JOIN
// against `f.id`. The L1 happy-path test supplies a minimal owner-only
// SELECT; M1 produces the production shape.
type FilterCTE struct {
	SQL  string
	Args []any
}

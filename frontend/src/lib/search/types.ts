// Search v1 wire types — manually authored for T1 because the backend
// only registers /api/v1/search* when Deps.Search is wired in (the
// OpenAPI dumper passes Deps{} so these routes are absent from
// openapi.json). Once the dumper picks them up these types should be
// re-derived from the generated schema.
//
// The shape mirrors internal/httpapi/search.go (searchBody,
// searchResultDTO, scoreComponentsDTO).

// SearchSort is the raw sort the client requests. The engine may
// coerce it (e.g. relevance + empty Q → newest); the *applied* sort
// is reflected in SearchResponse.effective_sort.
export type SearchSort = "relevance" | "newest" | "oldest";

// SearchFilters mirrors the bound query-string surface of GET
// /api/v1/search. tags is always an array (omitting it sends no `tag`
// params); the rest are optional and only forwarded when set.
export interface SearchFilters {
  dateAfter?: string;
  dateBefore?: string;
  tags: { tag_key: string; tag_label: string }[];
  location?: { location_label: string };
  mediaType?: "photo" | "video";
}

// SearchScoreComponents is the diagnostics-mode payload. RRF is the
// fused score (always present when explain=true). The per-signal
// pieces are nullable because not every hit matched both signals.
export interface SearchScoreComponents {
  rrf: number;
  bm25: number | null;
  vector: number | null;
  rank_bm25: number | null;
  rank_vector: number | null;
}

// SearchResult is one row in the response. Fields mirror the backend
// searchResultDTO; score and score_components are only populated when
// the request was issued with explain=true (and the AI Inspection
// setting is on).
export interface SearchResult {
  media_id: string;
  media_type: "photo" | "video";
  timestamp: string | null;
  imported_at: string;
  width: number | null;
  height: number | null;
  thumb_version: number;
  score?: number;
  score_components?: SearchScoreComponents;
}

// SearchResponse is the wire shape of the /api/v1/search response.
// next_cursor is the opaque token for the next page (null when there
// is no further page). has_more is the engine's signal that another
// page would not be empty; total is optional (server may omit when
// the result set is unbounded). embedding_completeness is the input
// for the "X% indexed" pill; the two semantic_unavailable_* fields
// drive the banner that explains why the request fell back to BM25.
export interface SearchResponse {
  results: SearchResult[];
  next_cursor: string | null;
  has_more: boolean;
  total?: number;
  effective_sort: SearchSort;
  embedding_completeness: number;
  semantic_unavailable: boolean;
  semantic_unavailable_reason:
    | ""
    | "no_active_generation"
    | "query_embedding_failed";
}

// TagSuggestion is one row in the autocomplete-tags response. key is
// the canonical tag_key, label is the display tag_label, count is the
// number of caller-visible media that carry it.
export interface TagSuggestion {
  key: string;
  label: string;
  count: number;
}

// LocationSuggestion is one row in the autocomplete-locations response.
// Locations don't carry a canonical key — the engine matches on the
// label directly — so the wire shape is (label, count) only.
export interface LocationSuggestion {
  label: string;
  count: number;
}

// AutocompleteTagsResponse is the wire shape for
// GET /api/v1/search/autocomplete/tags.
export interface AutocompleteTagsResponse {
  tags: TagSuggestion[];
}

// AutocompleteLocationsResponse is the wire shape for
// GET /api/v1/search/autocomplete/locations.
export interface AutocompleteLocationsResponse {
  locations: LocationSuggestion[];
}

// SearchRequestParams is the client-side projection of searchInput.
// Optional fields are only serialized when set — empty strings, zero
// times, and zero limits are dropped at the client boundary so the
// server sees exactly the params the user chose.
export interface SearchRequestParams {
  q?: string;
  sort?: SearchSort;
  date_after?: string;
  date_before?: string;
  tag?: string[];
  location?: string;
  media_type?: "photo" | "video";
  limit?: number;
  cursor?: string;
  include_hidden?: boolean;
  explain?: boolean;
}

import { SearchHTTPError, type SearchClient } from "./client";
import type {
  SearchFilters,
  SearchRequestParams,
  SearchResponse,
  SearchResult,
  SearchSort,
} from "./types";

// SearchStore is the public surface bound to the SearchPage component.
// All state is read-only on the public surface; mutations go through
// the named methods below so the store can manage cursor/inflight
// invariants centrally.
export interface SearchStore {
  readonly query: string;
  readonly filters: SearchFilters;
  readonly sort: SearchSort;
  readonly results: SearchResult[];
  readonly cursor: string | null;
  readonly hasMore: boolean;
  readonly total: number | null;
  readonly embeddingCompleteness: number;
  readonly semanticUnavailable: boolean;
  readonly semanticUnavailableReason: SearchResponse["semantic_unavailable_reason"];
  readonly effectiveSort: SearchSort;
  readonly requestHash: string | null;
  readonly loading: boolean;
  setQuery(q: string): Promise<void>;
  setFilters(f: SearchFilters): Promise<void>;
  setSort(s: SearchSort): Promise<void>;
  // setRequestShape mutates query/sort/filters atomically and issues
  // exactly one fetch. Used by Search.svelte's hydration $effect, where
  // calling setFilters → setSort → setQuery would fire three requests
  // (the prior two get aborted, but they still leave the browser and
  // — depending on backend ctx propagation — may cost server work).
  // Direct UI interactions (popover writes, sort segment clicks) keep
  // calling the single-dimensional setters since one-axis changes are
  // intentional there.
  setRequestShape(shape: { query: string; sort: SearchSort; filters: SearchFilters }): Promise<void>;
  fetchNextPage(): Promise<void>;
  onGenerationActivated(): void;
}

// emptyFilters is the canonical "no filters set" shape. Used at
// construction and to reset the store on a fresh search. Exported so
// Search.svelte can build a SearchFilters from the route match without
// duplicating the array-default invariants (every multi-value field
// starts as an empty array, never undefined).
export function emptyFilters(): SearchFilters {
  return { tags: [], cameras: [], lenses: [], facetTagKeys: [] };
}

// computeRequestHash derives a client-side cache token from the
// request shape. The backend mints its own server-side hash inside
// the cursor's ReqHash; this hash is purely a UI signal so the store
// can detect "the request shape that produced these results changed"
// without decoding the opaque cursor. Stable across tag-key
// permutations to match the backend's normalization.
function computeRequestHash(query: string, sort: SearchSort, filters: SearchFilters): string {
  const tagKeys = filters.tags.map((t) => t.tag_key).slice().sort();
  // The four SF-18 sidebar fields are sorted on the way in so the
  // client-side cache token is stable across click-order permutations
  // (the sidebar may yield ["B","A"] one render and ["A","B"] another;
  // both should map to the same hash).
  const cameras = filters.cameras.slice().sort();
  const lenses = filters.lenses.slice().sort();
  const facetTagKeys = filters.facetTagKeys.slice().sort();
  return JSON.stringify({
    q: query,
    s: sort,
    da: filters.dateAfter ?? "",
    db: filters.dateBefore ?? "",
    tk: tagKeys,
    loc: filters.location?.location_label ?? "",
    mt: filters.mediaType ?? "",
    ih: filters.includeHidden ?? false,
    cam: cameras,
    lens: lenses,
    ftk: facetTagKeys,
    hg: filters.hasGps ?? null,
  });
}

// buildParams projects the store's reactive state into the wire shape.
// dateAfter / dateBefore are forwarded as-is — the caller is expected
// to pass RFC3339 strings (the SearchFiltersPopover will produce these).
// explain is only forwarded when the per-user AI Inspection setting is
// on; the backend re-checks the same flag so a tampered request without
// the toggle still receives a non-diagnostic response.
function buildParams(
  query: string,
  sort: SearchSort,
  filters: SearchFilters,
  cursor: string | null,
  explain: boolean,
): SearchRequestParams {
  const params: SearchRequestParams = {};
  if (query !== "") params.q = query;
  if (sort !== undefined) params.sort = sort;
  if (filters.dateAfter != null && filters.dateAfter !== "") {
    params.date_after = filters.dateAfter;
  }
  if (filters.dateBefore != null && filters.dateBefore !== "") {
    params.date_before = filters.dateBefore;
  }
  if (filters.tags.length > 0) {
    params.tag = filters.tags.map((t) => t.tag_label);
  }
  if (filters.location?.location_label != null && filters.location.location_label !== "") {
    params.location = filters.location.location_label;
  }
  if (filters.mediaType !== undefined) params.media_type = filters.mediaType;
  if (filters.includeHidden === true) params.include_hidden = true;
  // SF-18 sidebar facets. Empty arrays are dropped so a zero-cameras
  // request doesn't ship a bare `?camera=` (which would 422 huma-side
  // anyway, but skipping at the boundary keeps the wire clean).
  if (filters.cameras.length > 0) params.camera = filters.cameras;
  if (filters.lenses.length > 0) params.lens = filters.lenses;
  if (filters.facetTagKeys.length > 0) params.facet_tag = filters.facetTagKeys;
  if (filters.hasGps !== undefined) params.has_gps = filters.hasGps;
  if (cursor !== null) params.cursor = cursor;
  if (explain) params.explain = true;
  return params;
}

// isCursorMismatch checks whether an error from .search() is a
// cursor-shape failure that should trigger a retry from null. The
// backend wraps mismatches in errs.ErrInvalidArgument → 400; we treat
// any 400 to /search as a cursor-mismatch cue. This is intentionally
// permissive — a future 400 from a different cause (e.g. invalid
// limit) would also restart pagination, which is harmless: the next
// request will surface the same failure and the user sees the empty
// state. Surfacing 400s via unhandled exception is the wrong UX.
function isCursorMismatch(e: unknown): boolean {
  return e instanceof SearchHTTPError && e.status === 400;
}

export interface CreateSearchStoreOptions {
  client: SearchClient;
  // explain is read on each call to issue() so a route component can
  // bind the flag to a reactive store (the AIInspectionStore). The
  // default is a constant `false` getter for tests and any caller that
  // doesn't wire the toggle. The function-as-getter shape lets the
  // store consult the live setting on every search rather than
  // capturing a value at construction; flipping the toggle takes
  // effect on the next setQuery / setFilters / setSort.
  explain?: () => boolean;
}

// createSearchStore builds a Svelte 5 rune-backed store. The function
// pattern (rather than a class) lets `$state` runes live at the top
// level and exposes them as getters on the returned object, which
// matches the plan's signature and keeps the public surface read-only
// from consumers.
export function createSearchStore(opts: CreateSearchStoreOptions): SearchStore {
  const client = opts.client;
  const explainGetter = opts.explain ?? (() => false);

  let query = $state<string>("");
  let filters = $state<SearchFilters>(emptyFilters());
  let sort = $state<SearchSort>("newest");
  let results = $state<SearchResult[]>([]);
  let cursor = $state<string | null>(null);
  let hasMore = $state<boolean>(false);
  let total = $state<number | null>(null);
  let embeddingCompleteness = $state<number>(0);
  let semanticUnavailable = $state<boolean>(false);
  let semanticUnavailableReason =
    $state<SearchResponse["semantic_unavailable_reason"]>("");
  let effectiveSort = $state<SearchSort>("newest");
  let requestHash = $state<string | null>(null);
  let loading = $state<boolean>(false);

  // Inflight management: every issue() bumps the token, captures it,
  // aborts any prior in-flight request, and drops its own response if
  // the token has moved on by the time it lands. abort() is called
  // only on the prior controller — never on `this` request — so a
  // race between two setQuery calls can't deadlock.
  let inflightToken = 0;
  let inflightController: AbortController | null = null;

  // issue runs a search request with the current state plus an
  // optional explicit cursor (defaulting to the current cursor).
  // Aborts any prior request before starting and writes the response
  // into store state only if its token is still current. Returns the
  // response on success or null when the request was aborted /
  // superseded; callers (fetchNextPage) inspect the return value to
  // decide whether to chain a follow-up.
  async function issue(reqCursor: string | null): Promise<SearchResponse | null> {
    if (inflightController) {
      try {
        inflightController.abort();
      } catch {
        // AbortController.abort() is documented to never throw, but
        // some polyfilled environments do. Swallow defensively.
      }
    }
    const ctrl = new AbortController();
    inflightController = ctrl;
    const token = ++inflightToken;
    loading = true;

    const params = buildParams(query, sort, filters, reqCursor, explainGetter());
    let res: SearchResponse;
    try {
      res = await client.search(params, ctrl.signal);
    } catch (e) {
      if (token !== inflightToken) return null;
      // Cursor mismatch on a cursor request → restart from null. We
      // recurse once with cursor=null and surface that response, so
      // the user sees fresh page-1 results instead of an error.
      if (reqCursor !== null && isCursorMismatch(e)) {
        cursor = null;
        // Recurse: the recursive call will bump the token and reset
        // loading. Don't gate on token === inflightToken here because
        // the recursive issue() does that itself.
        return issue(null);
      }
      // Any other error: clear loading on the active token and rethrow
      // to surface in the caller's promise. setQuery / fetchNextPage
      // catch nothing, so the rejection propagates to the route.
      loading = false;
      throw e;
    }
    if (token !== inflightToken) return null;

    // First response of a fresh search (cursor=null in the request
    // means "page 1" semantics — the prior results must be cleared).
    if (reqCursor === null) {
      results = res.results;
    } else {
      results = [...results, ...res.results];
    }
    cursor = res.next_cursor;
    hasMore = res.has_more;
    total = res.total ?? null;
    embeddingCompleteness = res.embedding_completeness;
    semanticUnavailable = res.semantic_unavailable;
    semanticUnavailableReason = res.semantic_unavailable_reason;
    effectiveSort = res.effective_sort;
    requestHash = computeRequestHash(query, sort, filters);
    loading = false;
    return res;
  }

  async function setQuery(q: string): Promise<void> {
    query = q;
    cursor = null;
    await issue(null);
  }

  async function setFilters(f: SearchFilters): Promise<void> {
    filters = f;
    cursor = null;
    await issue(null);
  }

  async function setSort(s: SearchSort): Promise<void> {
    sort = s;
    cursor = null;
    await issue(null);
  }

  async function setRequestShape(shape: {
    query: string;
    sort: SearchSort;
    filters: SearchFilters;
  }): Promise<void> {
    query = shape.query;
    sort = shape.sort;
    filters = shape.filters;
    cursor = null;
    await issue(null);
  }

  async function fetchNextPage(): Promise<void> {
    if (cursor === null) return;
    if (!hasMore) return;
    await issue(cursor);
  }

  // onGenerationActivated is called by the SSE pipe when the backend
  // emits ai.embed.generation_activated. The activation changes the
  // semantic-unavailable state and (for hybrid sorts) the result
  // ranking — the prior cursor's ReqHash no longer matches a fresh
  // request, and any cached results should be considered stale. We
  // clear requestHash so the next interaction (re-issue, navigation
  // back to /search) recognises the staleness; we also clear cursor
  // and hasMore so a load-more after activation cannot issue with the
  // now-stale cursor (fetchNextPage reads cursor directly). We
  // deliberately do NOT reissue the request here because the user may
  // have navigated away. The SearchPage's effect will re-run when
  // requestHash transitions to null.
  function onGenerationActivated(): void {
    requestHash = null;
    cursor = null;
    hasMore = false;
  }

  return {
    get query() { return query; },
    get filters() { return filters; },
    get sort() { return sort; },
    get results() { return results; },
    get cursor() { return cursor; },
    get hasMore() { return hasMore; },
    get total() { return total; },
    get embeddingCompleteness() { return embeddingCompleteness; },
    get semanticUnavailable() { return semanticUnavailable; },
    get semanticUnavailableReason() { return semanticUnavailableReason; },
    get effectiveSort() { return effectiveSort; },
    get requestHash() { return requestHash; },
    get loading() { return loading; },
    setQuery,
    setFilters,
    setSort,
    setRequestShape,
    fetchNextPage,
    onGenerationActivated,
  };
}

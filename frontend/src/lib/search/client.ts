import type {
  AutocompleteLocationsResponse,
  AutocompleteTagsResponse,
  SearchRequestParams,
  SearchResponse,
} from "./types";

// SearchHTTPError is the typed shape we throw on a non-2xx response so
// callers (the store) can tell a 400 cursor-mismatch from any other
// error without parsing free-text. status is the HTTP status code; body
// is the parsed JSON error body when available, raw text otherwise.
export class SearchHTTPError extends Error {
  status: number;
  body: unknown;
  constructor(status: number, message: string, body: unknown) {
    super(message);
    this.name = "SearchHTTPError";
    this.status = status;
    this.body = body;
  }
}

// SearchClient is the public surface the searchStore consumes. It is
// declared here (rather than inferred from a class) so test mocks can
// implement the interface directly without dragging in fetch.
export interface SearchClient {
  search(params: SearchRequestParams, signal?: AbortSignal): Promise<SearchResponse>;
  autocompleteTags(
    params: { prefix: string; limit?: number; include_hidden?: boolean },
    signal?: AbortSignal,
  ): Promise<AutocompleteTagsResponse>;
  autocompleteLocations(
    params: { substring: string; limit?: number; include_hidden?: boolean },
    signal?: AbortSignal,
  ): Promise<AutocompleteLocationsResponse>;
}

// buildSearchQuery serializes SearchRequestParams into a URLSearchParams
// instance. Repeated `tag` values use the `?tag=a&tag=b` form to match
// the backend's `query:"tag,explode"` binding. Empty / undefined values
// are dropped — the server treats absent params as "no filter", which
// matches the SPA's intent.
function buildSearchQuery(p: SearchRequestParams): URLSearchParams {
  const q = new URLSearchParams();
  if (p.q != null && p.q !== "") q.set("q", p.q);
  if (p.sort != null) q.set("sort", p.sort);
  if (p.date_after != null && p.date_after !== "") q.set("date_after", p.date_after);
  if (p.date_before != null && p.date_before !== "") q.set("date_before", p.date_before);
  if (p.tag) {
    for (const t of p.tag) {
      if (t !== "") q.append("tag", t);
    }
  }
  if (p.location != null && p.location !== "") q.set("location", p.location);
  if (p.media_type != null) q.set("media_type", p.media_type);
  if (p.limit != null && p.limit > 0) q.set("limit", String(p.limit));
  if (p.cursor != null && p.cursor !== "") q.set("cursor", p.cursor);
  if (p.include_hidden) q.set("include_hidden", "true");
  if (p.explain) q.set("explain", "true");
  return q;
}

// readErrorBody best-efforts a response body into a parsed shape so a
// callsite can inspect status/message without re-parsing. JSON parse
// failures fall back to raw text rather than a synthetic object — the
// caller already has res.status, and a non-JSON body is informational
// at best.
async function readErrorBody(res: Response): Promise<unknown> {
  const text = await res.text();
  if (text === "") return null;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

// FetchLike narrows the global fetch surface to what the client needs.
// Tests inject a mock via createSearchClient({ fetch }); production uses
// the global fetch.
export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface SearchClientOptions {
  fetch?: FetchLike;
  baseUrl?: string;
}

// createSearchClient builds a SearchClient backed by direct fetch. The
// /search routes are not in the generated openapi schema yet (the
// dumper passes Deps{} so they're absent), so we hand-roll the wire
// path rather than going through openapi-fetch. The shape mirrors
// internal/httpapi/search.go and the typed responses come from
// ./types.ts.
export function createSearchClient(opts: SearchClientOptions = {}): SearchClient {
  const fetchFn: FetchLike = opts.fetch ?? ((input, init) => fetch(input, init));
  const base = opts.baseUrl ?? "";

  async function get<T>(path: string, query: URLSearchParams, signal?: AbortSignal): Promise<T> {
    const qs = query.toString();
    const url = `${base}${path}${qs === "" ? "" : `?${qs}`}`;
    const init: RequestInit = { method: "GET", headers: { Accept: "application/json" } };
    if (signal !== undefined) init.signal = signal;
    const res = await fetchFn(url, init);
    if (!res.ok) {
      const body = await readErrorBody(res);
      const msg = typeof body === "object" && body !== null && "message" in body
        && typeof (body as { message?: unknown }).message === "string"
          ? (body as { message: string }).message
          : `${path} returned ${res.status}`;
      throw new SearchHTTPError(res.status, msg, body);
    }
    return (await res.json()) as T;
  }

  return {
    search(params, signal) {
      return get<SearchResponse>("/api/v1/search", buildSearchQuery(params), signal);
    },
    autocompleteTags(params, signal) {
      const q = new URLSearchParams();
      q.set("prefix", params.prefix);
      if (params.limit != null && params.limit > 0) q.set("limit", String(params.limit));
      if (params.include_hidden) q.set("include_hidden", "true");
      return get<AutocompleteTagsResponse>("/api/v1/search/autocomplete/tags", q, signal);
    },
    autocompleteLocations(params, signal) {
      const q = new URLSearchParams();
      q.set("substring", params.substring);
      if (params.limit != null && params.limit > 0) q.set("limit", String(params.limit));
      if (params.include_hidden) q.set("include_hidden", "true");
      return get<AutocompleteLocationsResponse>(
        "/api/v1/search/autocomplete/locations",
        q,
        signal,
      );
    },
  };
}

// searchClient is the default singleton bound to same-origin fetch.
// Mirrors the api singleton in lib/api/client.ts.
export const searchClient: SearchClient = createSearchClient();

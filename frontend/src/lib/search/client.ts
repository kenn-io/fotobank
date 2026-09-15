import * as generated from "../api/generated/client";
import type { SearchParams } from "../api/generated/models";
import type { APIResult } from "../api/transport";
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

export type FetchLike = typeof fetch;
export interface SearchClientOptions {
  fetch?: FetchLike;
  baseUrl?: string;
}

export function createSearchClient(opts: SearchClientOptions = {}): SearchClient {
  async function result<T>(response: APIResult<T>): Promise<T> {
    if (response.error) {
      const body = response.error;
      throw new SearchHTTPError(response.response.status, body.detail ?? body.title ?? `Search returned ${response.response.status}`, body);
    }
    return response.data as T;
  }
  return {
    async search(params, signal) {
      const { has_gps, ...rest } = params;
      const query: SearchParams = rest;
      if (has_gps !== undefined) query.has_gps = has_gps ? "true" : "false";
      return await result(await generated.search(query, { ...opts, ...(signal ? { signal } : {}) })) as SearchResponse;
    },
    async autocompleteTags(params, signal) {
      return await result(await generated.searchAutocompleteTags(params, { ...opts, ...(signal ? { signal } : {}) })) as AutocompleteTagsResponse;
    },
    async autocompleteLocations(params, signal) {
      return await result(await generated.searchAutocompleteLocations(params, { ...opts, ...(signal ? { signal } : {}) })) as AutocompleteLocationsResponse;
    },
  };
}

export const searchClient: SearchClient = createSearchClient();

import { describe, it, expect, vi } from "vitest";
import { createSearchStore, emptyFilters } from "./searchStore.svelte";
import { SearchHTTPError, type SearchClient } from "./client";
import type { SearchResponse } from "./types";

// canned builds a SearchResponse with sane defaults so tests can override
// only the fields they care about.
function canned(over: Partial<SearchResponse> = {}): SearchResponse {
  return {
    results: [],
    next_cursor: null,
    has_more: false,
    effective_sort: "newest",
    embedding_completeness: 0,
    semantic_unavailable: false,
    semantic_unavailable_reason: "",
    ...over,
  };
}

// makeClient builds a minimal SearchClient mock whose .search returns
// the canned response. Each call's params are recorded on `calls`.
function makeClient(responses: SearchResponse[] | (() => SearchResponse)) {
  const calls: Array<Record<string, unknown>> = [];
  let i = 0;
  const next = () => {
    if (typeof responses === "function") return responses();
    const r = responses[i] ?? responses[responses.length - 1]!;
    i++;
    return r;
  };
  const client: SearchClient = {
    search: vi.fn(async (params, _signal) => {
      calls.push(params as unknown as Record<string, unknown>);
      return next();
    }),
    autocompleteTags: vi.fn(async () => ({ tags: [] })),
    autocompleteLocations: vi.fn(async () => ({ locations: [] })),
  };
  return { client, calls };
}

describe("searchStore", () => {
  it("setQuery resets cursor and re-fetches", async () => {
    // First page comes back with a cursor and has_more=true so the
    // store has state worth resetting; the second response comes from
    // the setQuery() re-fetch and must have been issued with cursor=null.
    const { client, calls } = makeClient([
      canned({
        results: [{
          media_id: "a",
          media_type: "photo",
          timestamp: "2025-01-01T00:00:00Z",
          imported_at: "2025-01-01T00:00:00Z",
          width: 100,
          height: 100,
          thumb_version: 1,
        thumb_status: "ready",
        }],
        next_cursor: "cursor-page-1",
        has_more: true,
      }),
      canned({ results: [], next_cursor: null, has_more: false }),
    ]);
    const store = createSearchStore({ client });
    await store.setQuery("first");
    expect(store.cursor).toBe("cursor-page-1");
    expect(store.results.length).toBe(1);

    await store.setQuery("second");
    // Cursor is reset before the new fetch and stays null after the
    // empty response. Results from the first query must not leak
    // into the second.
    expect(store.cursor).toBe(null);
    expect(store.results.length).toBe(0);

    // The second search call must have been issued with cursor unset
    // (i.e. starting from scratch), not the stale page-1 cursor.
    expect(calls.length).toBe(2);
    const params2 = calls[1] as { q?: string; cursor?: string };
    expect(params2.q).toBe("second");
    expect(params2.cursor ?? "").toBe("");
  });

  it("SSE ai.embed.generation_activated invalidates requestHash", async () => {
    // Drive a request so requestHash is populated, then fire
    // onGenerationActivated and confirm the hash is cleared. The hash
    // is the client-side cache-invalidation token; clearing it tells
    // the next interaction that prior cursor/results are now stale.
    const { client } = makeClient([
      canned({ results: [], next_cursor: "c1", has_more: true }),
    ]);
    const store = createSearchStore({ client });
    await store.setQuery("dogs");
    expect(store.requestHash).not.toBeNull();
    store.onGenerationActivated();
    expect(store.requestHash).toBeNull();
  });

  it("onGenerationActivated clears cursor and hasMore", async () => {
    // fetchNextPage reads cursor directly, so leaving it set after a
    // generation flip would issue a load-more with the stale cursor —
    // exactly the ReqHash-mismatch the activation is meant to avoid.
    // Drive a request that returns a cursor with has_more=true so the
    // store has both fields populated, then fire the activation and
    // assert cursor/hasMore reset.
    const { client } = makeClient([
      canned({ results: [], next_cursor: "c1", has_more: true }),
    ]);
    const store = createSearchStore({ client });
    await store.setQuery("dogs");
    expect(store.cursor).toBe("c1");
    expect(store.hasMore).toBe(true);
    store.onGenerationActivated();
    expect(store.cursor).toBeNull();
    expect(store.hasMore).toBe(false);
  });

  it("cursor mismatch (server returns 400) restarts pagination from null", async () => {
    // Page 1 succeeds and gives us a cursor. fetchNextPage with that
    // cursor 400s (cursor-mismatch). The store must clear the cursor
    // and re-issue the request from null instead of looping with the
    // stale cursor or surfacing the error to the user.
    let callCount = 0;
    const client: SearchClient = {
      search: vi.fn(async (params, _signal) => {
        callCount++;
        const cursor = (params as { cursor?: string }).cursor ?? "";
        if (callCount === 1) {
          return canned({
            results: [{
              media_id: "p1",
              media_type: "photo",
              timestamp: "2025-01-01T00:00:00Z",
              imported_at: "2025-01-01T00:00:00Z",
              width: 100,
              height: 100,
              thumb_version: 1,
        thumb_status: "ready",
            }],
            next_cursor: "stale-cursor",
            has_more: true,
          });
        }
        if (callCount === 2 && cursor === "stale-cursor") {
          // Backend returns 400 ErrInvalidArgument when the cursor's
          // ReqHash doesn't match the current request shape; the
          // client surface throws SearchHTTPError on non-2xx.
          throw new SearchHTTPError(400, "invalid argument: validate cursor", {
            message: "invalid argument: validate cursor",
          });
        }
        // Restart from null: empty result is fine, the test only cares
        // that the store recovered without surfacing the 400.
        return canned({ results: [], next_cursor: null, has_more: false });
      }),
      autocompleteTags: vi.fn(async () => ({ tags: [] })),
      autocompleteLocations: vi.fn(async () => ({ locations: [] })),
    };
    const store = createSearchStore({ client });
    await store.setQuery("forest");
    expect(store.cursor).toBe("stale-cursor");
    await store.fetchNextPage();
    // After the 400, the store must have restarted: cursor cleared
    // and the third call issued without the stale cursor.
    expect(store.cursor).toBe(null);
    expect(callCount).toBe(3);
  });

  it("setRequestShape issues exactly one fetch with combined params", async () => {
    // The Search.svelte hydration effect used to call setFilters →
    // setSort → setQuery on every URL change, which fires three
    // /search requests (the first two are aborted via AbortController,
    // but they still leave the browser and may cost backend work
    // depending on ctx propagation). setRequestShape collapses that
    // cascade into one issue() and one client.search() — this test
    // pins that contract so a regression that re-introduces the
    // cascade surfaces here.
    const { client, calls } = makeClient([canned()]);
    const store = createSearchStore({ client });
    await store.setRequestShape({
      query: "dogs",
      sort: "relevance",
      filters: {
        ...emptyFilters(),
        cameras: ["Sony A7R IV"],
        lenses: ["FE 24-70mm F2.8 GM"],
        facetTagKeys: ["dog"],
        hasGps: true,
        mediaType: "photo",
      },
    });
    expect(calls.length).toBe(1);
    const params = calls[0] as {
      q?: string;
      sort?: string;
      camera?: string[];
      lens?: string[];
      facet_tag?: string[];
      has_gps?: boolean;
      media_type?: string;
    };
    expect(params.q).toBe("dogs");
    expect(params.sort).toBe("relevance");
    expect(params.camera).toEqual(["Sony A7R IV"]);
    expect(params.lens).toEqual(["FE 24-70mm F2.8 GM"]);
    expect(params.facet_tag).toEqual(["dog"]);
    expect(params.has_gps).toBe(true);
    expect(params.media_type).toBe("photo");
    // Store state reflects all three axes from the single call.
    expect(store.query).toBe("dogs");
    expect(store.sort).toBe("relevance");
    expect(store.filters.cameras).toEqual(["Sony A7R IV"]);
  });

  it("setRequestShape resets cursor before re-issuing", async () => {
    // Page 1 returns a cursor; setRequestShape must clear it before
    // firing the next request so the second call starts from scratch
    // (the request shape changed, the prior cursor is invalid).
    const { client, calls } = makeClient([
      canned({ next_cursor: "stale-cursor", has_more: true }),
      canned({ results: [], next_cursor: null, has_more: false }),
    ]);
    const store = createSearchStore({ client });
    await store.setQuery("first");
    expect(store.cursor).toBe("stale-cursor");

    await store.setRequestShape({
      query: "second",
      sort: "newest",
      filters: emptyFilters(),
    });
    expect(store.cursor).toBeNull();
    const params2 = calls[1] as { q?: string; cursor?: string };
    expect(params2.q).toBe("second");
    expect(params2.cursor ?? "").toBe("");
  });

  it("setFilters forwards camera/lens/facet_tag/has_gps/media_type to client.search", async () => {
    // SF-18 added cameras/lenses/facetTagKeys/hasGps to SearchFilters
    // and threaded them through buildParams. The existing tests scaffold
    // the empty arrays but never assert the populated fields actually
    // round-trip. This test pins the wire shape so a regression in
    // buildParams (forgetting to forward a field, dropping the
    // serialization) surfaces here rather than in production.
    const { client, calls } = makeClient([canned()]);
    const store = createSearchStore({ client });
    await store.setFilters({
      ...emptyFilters(),
      cameras: ["Sony A7R IV"],
      lenses: ["FE 24-70mm F2.8 GM"],
      facetTagKeys: ["dog"],
      hasGps: true,
      mediaType: "photo",
    });
    expect(calls.length).toBe(1);
    const params = calls[0] as {
      camera?: string[];
      lens?: string[];
      facet_tag?: string[];
      has_gps?: boolean;
      media_type?: string;
    };
    expect(params.camera).toEqual(["Sony A7R IV"]);
    expect(params.lens).toEqual(["FE 24-70mm F2.8 GM"]);
    expect(params.facet_tag).toEqual(["dog"]);
    expect(params.has_gps).toBe(true);
    expect(params.media_type).toBe("photo");
  });
});

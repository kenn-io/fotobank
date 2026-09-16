import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest";
import { render } from "@testing-library/svelte";
import { flushSync, tick } from "svelte";
import Search from "./Search.svelte";
import type { SearchStore } from "../lib/search/searchStore.svelte";
import type { SearchClient } from "../lib/search/client";
import type { SearchFilters, SearchResult, SearchSort } from "../lib/search/types";
import { router } from "../lib/router/router.svelte";
import { AIInspectionStore } from "../lib/ai/inspectionStore.svelte";
import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";

// VirtualGrid wires ResizeObserver + IntersectionObserver in $effect
// blocks. jsdom ships neither. The default IntersectionObserver stub
// is a noop; the loadMore test below installs a captureing variant
// that records the constructor callback so we can fire it directly.
beforeAll(() => {
  class NoopResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  class NoopIntersectionObserver {
    root = null;
    rootMargin = "";
    thresholds: number[] = [];
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords(): IntersectionObserverEntry[] { return []; }
  }
  vi.stubGlobal("ResizeObserver", NoopResizeObserver);
  vi.stubGlobal("IntersectionObserver", NoopIntersectionObserver);
});

beforeEach(() => {
  // Reset the URL between tests so router.current observes the route
  // /search rather than carrying over from a prior test. router.current
  // is computed once at module import; without an explicit sync the
  // page's onMount short-circuits on `m.route !== "search"`.
  window.history.replaceState({}, "", "/search");
  router.syncFromLocation();
});

afterEach(() => vi.restoreAllMocks());

// makeStore builds a SearchStore stub. Each method is a vi.fn() so
// individual tests can spy on the calls; getter values are the raw
// fields so tests can pre-seed query/results before mounting.
type StoreSeed = {
  query?: string;
  filters?: SearchFilters;
  sort?: SearchSort;
  results?: SearchResult[];
  cursor?: string | null;
  hasMore?: boolean;
  total?: number | null;
  embeddingCompleteness?: number;
  semanticUnavailable?: boolean;
  loading?: boolean;
};

function makeStore(seed: StoreSeed = {}): SearchStore {
  return {
    query: seed.query ?? "",
    filters: seed.filters ?? { tags: [], cameras: [], lenses: [], facetTagKeys: [] },
    sort: seed.sort ?? "relevance",
    results: seed.results ?? [],
    cursor: seed.cursor ?? null,
    hasMore: seed.hasMore ?? false,
    total: seed.total ?? null,
    embeddingCompleteness: seed.embeddingCompleteness ?? 0,
    semanticUnavailable: seed.semanticUnavailable ?? false,
    semanticUnavailableReason: "",
    effectiveSort: seed.sort ?? "relevance",
    requestHash: null,
    loading: seed.loading ?? false,
    setQuery: vi.fn().mockResolvedValue(undefined),
    setFilters: vi.fn().mockResolvedValue(undefined),
    setSort: vi.fn().mockResolvedValue(undefined),
    setRequestShape: vi.fn().mockResolvedValue(undefined),
    fetchNextPage: vi.fn().mockResolvedValue(undefined),
    onGenerationActivated: vi.fn(),
  } as unknown as SearchStore;
}

function makeClient(): SearchClient {
  return {
    search: vi.fn().mockResolvedValue({
      results: [],
      next_cursor: null,
      has_more: false,
      effective_sort: "relevance",
      embedding_completeness: 0,
      semantic_unavailable: false,
      semantic_unavailable_reason: "",
    }),
    autocompleteTags: vi.fn().mockResolvedValue({ tags: [] }),
    autocompleteLocations: vi.fn().mockResolvedValue({ locations: [] }),
  };
}

// makeInspectionStore returns an AIInspectionStore wired to a stub
// client result shape that immediately resolves an empty value, so
// load() flips the loaded flag before the page's hydration $effect
// would otherwise gate. Tests that don't care about the toggle's
// persisted value pass this directly; tests that drive a specific
// persisted value override the GET mock per-call.
function makeInspectionStore(getValue: string | undefined = undefined): AIInspectionStore {
  const GET = vi.fn().mockResolvedValue(
    getValue !== undefined
      ? { data: { value: getValue }, error: undefined }
      : { data: undefined, error: undefined },
  );
  const PUT = vi.fn().mockResolvedValue({ error: undefined });
  return new AIInspectionStore({ GET, PUT ,
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); },
search(params?: any, options?: any) { return (this as any).GET("/api/v1/search", { params: { query: params }, ...options }); },
searchAutocompleteLocations(params?: any, options?: any) { return (this as any).GET("/api/v1/search/autocomplete/locations", { params: { query: params }, ...options }); },
searchAutocompleteTags(params?: any, options?: any) { return (this as any).GET("/api/v1/search/autocomplete/tags", { params: { query: params }, ...options }); },
getUserSetting(key?: any, options?: any) { return (this as any).GET("/api/v1/settings/user/{key}", { params: { path: { key } }, ...options }); },
putUserSetting(key?: any, userSettingPutInputBody?: any, options?: any) { return (this as any).PUT("/api/v1/settings/user/{key}", { params: { path: { key } }, body: userSettingPutInputBody, ...options }); }
} as never);
}

describe("Search.svelte", () => {
  it("renders the idle empty state when no query and no results", () => {
    const store = makeStore();
    const { container } = render(Search, {
      props: { store, client: makeClient(), inspectionStore: makeInspectionStore() },
    });
    // No search results and no query → the idle prompt renders, and
    // there are no media-cell anchors (the grid is suppressed).
    const idle = container.querySelector("[data-testid='search-idle-state']");
    expect(idle).not.toBeNull();
    expect(container.querySelectorAll("a[data-media-id]").length).toBe(0);
  });

  it("writes the store's state into the URL after hydration", async () => {
    // The page's URL-sync $effect projects (query, sort, filters) into
    // /search?... so the URL is the source of truth. The effect is
    // gated on a `hydrated` flag flipped at the end of onMount; the
    // first observable navigate is therefore the one that mirrors the
    // post-hydration store state.
    const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const store = makeStore({
      query: "trees",
      sort: "newest",
      filters: {
        tags: [{ tag_key: "dog", tag_label: "Dog" }],
        cameras: [],
        lenses: [],
        facetTagKeys: [],
        dateAfter: "2025-01-01",
        mediaType: "photo",
      },
    });
    const inspectionStore = makeInspectionStore();
    render(Search, { props: { store, client: makeClient(), inspectionStore } });
    // load() is async — wait until loaded flips so the hydration
    // $effect runs before assertions. flushSync alone isn't enough
    // because the GET resolves on a microtask after construction.
    await inspectionStore.load();
    // $effect runs as a microtask after mount; awaiting tick lets it
    // settle so the assertion sees the post-hydration navigate.
    flushSync();
    await tick();
    // The navigate target encodes every set field. Tags use tag_label
    // (not tag_key) per the O1 backend contract — searchInput.Tag is
    // resolved server-side as labels. relevance is the default and
    // gets omitted; "newest" is non-default and is written.
    expect(navigate).toHaveBeenCalled();
    const lastCall = navigate.mock.calls[navigate.mock.calls.length - 1]!;
    const target = lastCall[0] as string;
    const opts = lastCall[1] as { replace?: boolean } | undefined;
    expect(opts?.replace).toBe(true);
    const u = new URL(target, window.location.origin);
    expect(u.pathname).toBe("/search");
    expect(u.searchParams.get("q")).toBe("trees");
    expect(u.searchParams.get("sort")).toBe("newest");
    expect(u.searchParams.get("date_after")).toBe("2025-01-01");
    expect(u.searchParams.getAll("tag")).toEqual(["Dog"]);
    expect(u.searchParams.get("media_type")).toBe("photo");
  });

  it("omits sort=relevance from the URL (relevance is the default)", async () => {
    // The URL convention is: relevance is implicit. The canonical URL
    // for an empty-state /search is exactly /search with no query.
    const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const store = makeStore({ query: "trees", sort: "relevance" });
    const inspectionStore = makeInspectionStore();
    render(Search, { props: { store, client: makeClient(), inspectionStore } });
    await inspectionStore.load();
    flushSync();
    await tick();
    expect(navigate).toHaveBeenCalled();
    const lastCall = navigate.mock.calls[navigate.mock.calls.length - 1]!;
    const target = lastCall[0] as string;
    const u = new URL(target, window.location.origin);
    expect(u.searchParams.has("sort")).toBe(false);
    expect(u.searchParams.get("q")).toBe("trees");
  });

  it("rehydrates the store when router.current changes to a new ?q=", async () => {
    // Mount on /search?q=foo so the initial hydration captures q=foo.
    // Then update the URL to /search?q=bar and call router.syncFromLocation,
    // which mutates router.current. The hydration $effect must re-run
    // and route the new query through store.setRequestShape, otherwise
    // external URL changes (e.g. AppHeader typing while already on
    // /search) leave the store stale.
    window.history.replaceState({}, "", "/search?q=foo");
    router.syncFromLocation();
    const store = makeStore();
    const inspectionStore = makeInspectionStore();
    render(Search, { props: { store, client: makeClient(), inspectionStore } });
    await inspectionStore.load();
    flushSync();
    await tick();
    const setRequestShapeMock = store.setRequestShape as ReturnType<typeof vi.fn>;
    expect(setRequestShapeMock).toHaveBeenCalledWith(
      expect.objectContaining({ query: "foo" }),
    );

    // Simulate AppHeader navigating /search?q=foo → /search?q=bar.
    window.history.replaceState({}, "", "/search?q=bar");
    router.syncFromLocation();
    flushSync();
    await tick();
    expect(setRequestShapeMock).toHaveBeenCalledWith(
      expect.objectContaining({ query: "bar" }),
    );
  });

  it("triggers fetchNextPage when VirtualGrid's load-more sentinel intersects", async () => {
    // Capture the IntersectionObserver constructor callback so the test
    // can fire it directly. VirtualGrid creates one for resize, one for
    // load-more, and one for active-month tracking. The load-more one
    // observes a single sentinel <div> at the tail, so we drive every
    // captured callback with isIntersecting=true and rely on the
    // load-more callback being the one that calls onLoadMore (the
    // others are no-ops on a synthetic intersection event because they
    // either filter on root/rootMargin or read entry.target attributes).
    const callbacks: IntersectionObserverCallback[] = [];
    class CapturingIntersectionObserver implements IntersectionObserver {
      root: Element | Document | null = null;
      rootMargin = "";
      thresholds: number[] = [];
      constructor(cb: IntersectionObserverCallback) {
        callbacks.push(cb);
      }
      observe(): void {}
      unobserve(): void {}
      disconnect(): void {}
      takeRecords(): IntersectionObserverEntry[] { return []; }
    }
    vi.stubGlobal("IntersectionObserver", CapturingIntersectionObserver);

    // Seed one result so VirtualGrid renders (the empty-state branch
    // suppresses the grid entirely, which would also suppress the
    // sentinel and its observer).
    const store = makeStore({
      query: "trees",
      results: [{
        media_id: "m1",
        media_type: "photo",
        timestamp: "2025-06-01T00:00:00Z",
        imported_at: "2025-06-01T00:00:00Z",
        width: 1600,
        height: 1200,
        thumb_version: 1,
        thumb_status: "ready",
      }],
      cursor: "next-page",
      hasMore: true,
    });
    render(Search, {
      props: { store, client: makeClient(), inspectionStore: makeInspectionStore() },
    });

    // Fire each captured callback with a synthetic intersecting entry.
    // The load-more observer's callback only inspects
    // entries[0]?.isIntersecting; the resize observer is a different
    // class (ResizeObserver), and the active-month observer reads
    // entry.target.getAttribute("data-month"), which our stub element
    // doesn't have so it harmlessly skips. fetchNextPage is the only
    // observable side effect.
    const fakeEntry = {
      isIntersecting: true,
      target: document.createElement("div"),
    } as unknown as IntersectionObserverEntry;
    for (const cb of callbacks) {
      cb([fakeEntry], {} as IntersectionObserver);
    }

    expect(store.fetchNextPage).toHaveBeenCalled();
  });

  it("URL-sync echo does not re-hydrate the store", async () => {
    // The URL-sync $effect writes /search?q=trees via router.navigate,
    // which calls syncFromLocation() and mutates router.current. The
    // hydration $effect then observes the new router.current and would
    // run setRequestShape again unless lastSyncedKey recognises it as
    // the writeback's own echo. This test mounts with a pre-seeded
    // store, lets the URL-sync run real navigate() so router.current
    // changes, and asserts the post-writeback hydration does not call
    // setRequestShape again.
    //
    // Use the real router.navigate (no spy) so the URL writeback
    // actually mutates router.current. The setRequestShape count seen
    // by the test reflects (a) the initial hydration cycle for the
    // seeded URL, and (b) any echo-induced re-run.
    const store = makeStore({
      query: "trees",
      sort: "newest",
      filters: { tags: [], cameras: [], lenses: [], facetTagKeys: [] },
    });
    const inspectionStore = makeInspectionStore();
    render(Search, { props: { store, client: makeClient(), inspectionStore } });
    await inspectionStore.load();
    flushSync();
    await tick();

    // Assert setRequestShape was called EXACTLY once, with the seeded
    // URL's values (the URL was /search with no params, so the
    // hydration ran with q="", sort="relevance", and the canonical
    // empty-filters shape that filtersFromMatch produces). The URL-sync
    // then wrote /search?q=trees&sort=newest from the seeded store
    // state, which mutated router.current. If echo suppression failed,
    // we'd see a second hydration call here with q="trees" / sort=
    // "newest" — the assertion below would fail with a 2-count.
    const setRequestShapeMock = store.setRequestShape as ReturnType<typeof vi.fn>;
    expect(setRequestShapeMock.mock.calls).toEqual([[
      {
        query: "",
        sort: "relevance",
        filters: { tags: [], cameras: [], lenses: [], facetTagKeys: [] },
      },
    ]]);

    // Bump the URL via syncFromLocation again. The URL still matches
    // lastSyncedKey from the prior writeback, so hydration must
    // suppress this as an echo too — no new setter calls.
    router.syncFromLocation();
    flushSync();
    await tick();
    expect(setRequestShapeMock.mock.calls).toEqual([[
      {
        query: "",
        sort: "relevance",
        filters: { tags: [], cameras: [], lenses: [], facetTagKeys: [] },
      },
    ]]);
  });

  it("waits for AIInspection to load before the first search", async () => {
    // The AIInspectionStore's GET resolves on a microtask, which
    // historically meant the hydration $effect fired BEFORE load()
    // resolved. The explainGetter then read its default (false) and a
    // user with persisted ai.inspection=true received a non-diagnostic
    // page on the first render. Gate the hydration on
    // inspectionStore.loaded so the persisted value is honoured even
    // for the very first request.
    //
    // Mount on /search?q=cats so the route hydrates with a query, and
    // construct the inspection store with a deferred GET we resolve
    // after asserting the hydration hasn't fired yet. The real store
    // we create here drives a real createSearchStore via the
    // explainGetter, so we observe the effect through the
    // SearchClient's `search` calls instead of through a stub store's
    // setQuery — that way we can assert explain=true reaches the
    // wire.
    window.history.replaceState({}, "", "/search?q=cats");
    router.syncFromLocation();

    // resolveGet captures the deferred resolver for the very first GET
    // call (the route fires load() in its mount path). We don't want
    // to make a second call from the test — that would issue a fresh
    // deferred promise — so we keep the GET strict: only the first
    // invocation produces the deferred shape, any subsequent calls
    // resolve immediately to keep the test independent.
    let resolveGet: ((value: { data: { value: string } | undefined; error: undefined }) => void) | null = null;
    let getCalls = 0;
    const GET = vi.fn().mockImplementation(() => {
      getCalls++;
      if (getCalls === 1) {
        return new Promise((res) => {
          resolveGet = res as typeof resolveGet;
        });
      }
      return Promise.resolve({ data: undefined, error: undefined });
    });
    const PUT = vi.fn().mockResolvedValue({ error: undefined });
    const inspectionStore = new AIInspectionStore({ GET, PUT ,
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); },
search(params?: any, options?: any) { return (this as any).GET("/api/v1/search", { params: { query: params }, ...options }); },
searchAutocompleteLocations(params?: any, options?: any) { return (this as any).GET("/api/v1/search/autocomplete/locations", { params: { query: params }, ...options }); },
searchAutocompleteTags(params?: any, options?: any) { return (this as any).GET("/api/v1/search/autocomplete/tags", { params: { query: params }, ...options }); },
getUserSetting(key?: any, options?: any) { return (this as any).GET("/api/v1/settings/user/{key}", { params: { path: { key } }, ...options }); },
putUserSetting(key?: any, userSettingPutInputBody?: any, options?: any) { return (this as any).PUT("/api/v1/settings/user/{key}", { params: { path: { key } }, body: userSettingPutInputBody, ...options }); }
} as never);

    const client = makeClient();
    render(Search, { props: { client, inspectionStore } });
    flushSync();
    await tick();
    // Before load resolves, no search should have fired — the
    // hydration $effect is gated on inspectionStore.loaded.
    expect(client.search).not.toHaveBeenCalled();

    // Resolve the GET with the persisted "true" payload. The route's
    // in-flight load() promise then microtask-chains to set
    // this.loaded = true; we await two microtasks to let that
    // settle before the $effect schedules.
    expect(resolveGet).not.toBeNull();
    resolveGet!({ data: { value: "true" }, error: undefined });
    // Two ticks: one for the GET .then to run (sets loaded=true), one
    // for the resulting $effect run scheduled off that state change.
    await tick();
    flushSync();
    await tick();
    // The default-constructed search store inside Search.svelte ran
    // setRequestShape, which calls issue() once with the combined
    // shape; explain=true must be set on that call since the toggle's
    // persisted value loaded as true.
    expect(client.search).toHaveBeenCalled();
    const lastCall = (client.search as ReturnType<typeof vi.fn>).mock.calls.at(-1);
    expect(lastCall).toBeDefined();
    const params = lastCall![0] as { explain?: boolean };
    expect(params.explain).toBe(true);
  });

  it("opens a result in the lightbox with score_components on the snapshot", async () => {
    // V2 wiring: clicking a search result should capture the
    // score_components map onto the lightboxSession snapshot so the
    // metadata drawer's Search relevance row reads from it. Mount
    // Search with two results — one carrying score_components, one
    // without — fire a plain click on the first cell, and assert:
    //   1. The session source is { kind: "search" }.
    //   2. scoreComponentsById carries the first hit's components.
    //   3. router.navigate was called with /media/m1?from=search.
    //
    // Use a real selection store reset (clear from any prior test) so
    // useSelection narrowing doesn't kick in unexpectedly.
    lightboxSession.close();
    const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const sc1 = {
      rrf: 0.0156,
      bm25: 8.42,
      vector: 0.81,
      rank_bm25: 3,
      rank_vector: 7,
    };
    const store = makeStore({
      query: "trees",
      results: [
        {
          media_id: "m1",
          media_type: "photo",
          timestamp: "2025-06-01T00:00:00Z",
          imported_at: "2025-06-01T00:00:00Z",
          width: 1600,
          height: 1200,
          thumb_version: 1,
        thumb_status: "ready",
          score_components: sc1,
        },
        {
          media_id: "m2",
          media_type: "photo",
          timestamp: "2025-06-02T00:00:00Z",
          imported_at: "2025-06-02T00:00:00Z",
          width: 1600,
          height: 1200,
          thumb_version: 1,
        thumb_status: "ready",
        },
      ],
    });
    const { container } = render(Search, {
      props: { store, client: makeClient(), inspectionStore: makeInspectionStore() },
    });
    flushSync();
    await tick();

    const cell = container.querySelector<HTMLAnchorElement>(
      "a[data-media-id='m1']",
    );
    expect(cell).not.toBeNull();
    cell!.click();

    // The snapshot is captured synchronously inside the click handler;
    // assert immediately. snapshot.scoreComponentsById is a defensive
    // copy of the route's $derived map.
    const snap = lightboxSession.snapshot;
    expect(snap).not.toBeNull();
    expect(snap!.source).toEqual({ kind: "search" });
    expect(snap!.navIds).toEqual(["m1", "m2"]);
    expect(snap!.returnFocusMediaId).toBe("m1");
    const map = snap!.scoreComponentsById;
    expect(map).toBeDefined();
    expect(map!.get("m1")).toEqual(sc1);
    expect(map!.has("m2")).toBe(false);

    // The navigate target carries from=search and a qhash so the
    // lightbox can validate the snapshot against the active search
    // state. Don't pin to a specific hash value (that's coupled to
    // computeKey's JSON serialisation); just assert presence and
    // non-emptiness.
    const lightboxCall = navigate.mock.calls.find((c) =>
      typeof c[0] === "string" && (c[0] as string).startsWith("/media/m1"),
    );
    expect(lightboxCall).toBeDefined();
    const target = lightboxCall![0] as string;
    const u = new URL(target, window.location.origin);
    expect(u.pathname).toBe("/media/m1");
    expect(u.searchParams.get("from")).toBe("search");
    const qhash = u.searchParams.get("qhash");
    expect(qhash).not.toBeNull();
    expect(qhash!.length).toBeGreaterThan(0);
  });

  it("OpenMediaIncludesQHashInURL", async () => {
    // Stale-snapshot defense (paired with
    // Lightbox.test.ts::SearchSnapshotRejectedWhenQHashMismatches):
    // openMedia must mirror the snapshot's qHash onto the /media/:id
    // URL via ?qhash= so the lightbox can validate the snapshot
    // against the active search state. Without it, a direct entry to
    // /media/:id?from=search via shared URL or browser back could pick
    // up an unrelated stale snapshot. Mount with a configured query +
    // filters so the hash is non-trivial; click a cell and assert the
    // navigate target carries qhash with a non-empty value, AND the
    // snapshot's qHash matches what the URL carries.
    lightboxSession.close();
    const navigate = vi.spyOn(router, "navigate").mockImplementation(() => {});
    const store = makeStore({
      query: "trees",
      sort: "newest",
      filters: {
        tags: [{ tag_key: "dog", tag_label: "Dog" }],
        cameras: [],
        lenses: [],
        facetTagKeys: [],
        dateAfter: "2025-01-01",
        mediaType: "photo",
      },
      results: [
        {
          media_id: "m1",
          media_type: "photo",
          timestamp: "2025-06-01T00:00:00Z",
          imported_at: "2025-06-01T00:00:00Z",
          width: 1600,
          height: 1200,
          thumb_version: 1,
        thumb_status: "ready",
        },
      ],
    });
    const { container } = render(Search, {
      props: { store, client: makeClient(), inspectionStore: makeInspectionStore() },
    });
    flushSync();
    await tick();

    const cell = container.querySelector<HTMLAnchorElement>(
      "a[data-media-id='m1']",
    );
    expect(cell).not.toBeNull();
    cell!.click();

    // Find the lightbox-open navigate (skip URL-sync writebacks).
    const lightboxCall = navigate.mock.calls.find((c) =>
      typeof c[0] === "string" && (c[0] as string).startsWith("/media/m1"),
    );
    expect(lightboxCall).toBeDefined();
    const target = lightboxCall![0] as string;
    const u = new URL(target, window.location.origin);
    expect(u.searchParams.get("from")).toBe("search");
    const qhash = u.searchParams.get("qhash");
    expect(qhash).not.toBeNull();
    expect(qhash!.length).toBeGreaterThan(0);

    // Snapshot's qHash must agree with the URL's qhash so the lightbox
    // validation passes. Different inputs would obviously produce
    // different hashes, but the round-trip must be exact.
    const snap = lightboxSession.snapshot;
    expect(snap).not.toBeNull();
    expect(snap!.qHash).toBe(qhash);
  });
});

import { describe, it, expect, vi, beforeAll, beforeEach } from "vitest";
import { render } from "@testing-library/svelte";
import Search from "./Search.svelte";
import type { SearchStore } from "../lib/search/searchStore.svelte";
import type { SearchClient } from "../lib/search/client";
import type { SearchFilters, SearchResult, SearchSort } from "../lib/search/types";

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
  // /search rather than carrying over from a prior test.
  window.history.replaceState({}, "", "/search");
});

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
    filters: seed.filters ?? { tags: [] },
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

describe("Search.svelte", () => {
  it("renders the idle empty state when no query and no results", () => {
    const store = makeStore();
    const { container } = render(Search, {
      props: { store, client: makeClient() },
    });
    // No search results and no query → the idle prompt renders, and
    // there are no media-cell anchors (the grid is suppressed).
    const idle = container.querySelector("[data-testid='search-idle-state']");
    expect(idle).not.toBeNull();
    expect(container.querySelectorAll("a[data-media-id]").length).toBe(0);
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
      }],
      cursor: "next-page",
      hasMore: true,
    });
    render(Search, { props: { store, client: makeClient() } });

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
});

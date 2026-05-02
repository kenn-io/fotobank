<!-- frontend/src/routes/Search.svelte
     T2: page shell for /search. Reads the route match into the search
     store on mount and renders the filter / sort / status surface plus
     a result grid. The popover, chip strip, sort segment, and indexing
     pill/banner are stubbed at this checkpoint (see TODO U1/U2/V1
     comments in their files); this commit only proves the wiring,
     prop shapes, and pagination glue. -->
<script lang="ts">
  import { onMount } from "svelte";
  import { createSearchStore, type SearchStore } from "../lib/search/searchStore.svelte";
  import { searchClient } from "../lib/search/client";
  import type { SearchClient } from "../lib/search/client";
  import type { SearchFilters, SearchSort } from "../lib/search/types";
  import SearchFiltersPopover from "../lib/search/SearchFiltersPopover.svelte";
  import SearchFilterChips from "../lib/search/SearchFilterChips.svelte";
  import SearchSortSegment from "../lib/search/SearchSortSegment.svelte";
  import IndexingStatusPill from "../lib/search/IndexingStatusPill.svelte";
  import IndexingStatusBanner from "../lib/search/IndexingStatusBanner.svelte";
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import type { Month, Media } from "../lib/media/mediaStore.svelte";
  import { router } from "../lib/router/router.svelte";

  // Tests inject a stub store via the optional `store` prop; production
  // callers omit it and the route constructs its own backed by the
  // singleton search client. searchClient comes from the same module
  // that hosts createSearchClient (default same-origin fetch).
  let { store, client = searchClient }: {
    store?: SearchStore;
    client?: SearchClient;
  } = $props();

  // svelte-ignore state_referenced_locally
  const s: SearchStore = store ?? createSearchStore({ client });

  // filtersFromMatch projects the route match into the SearchFilters
  // shape the store expects. It is only called inside onMount with a
  // narrowed `match.route === "search"` pre-condition, so the typed
  // fields are safe to read directly. tags arrive on the wire as
  // tag_label strings (the ?tag= query repeats the label, not a
  // canonical key); without the canonical key we mirror label → key,
  // which the store dedupes by tag_key when it computes the request
  // hash.
  function filtersFromMatch(m: Extract<typeof router.current, { route: "search" }>): SearchFilters {
    const out: SearchFilters = { tags: [] };
    if (m.date_after !== undefined) out.dateAfter = m.date_after;
    if (m.date_before !== undefined) out.dateBefore = m.date_before;
    if (m.tag !== undefined) {
      out.tags = m.tag.map((label) => ({ tag_key: label, tag_label: label }));
    }
    if (m.location !== undefined) out.location = { location_label: m.location };
    if (m.media_type !== undefined) out.mediaType = m.media_type;
    return out;
  }

  // The popover, chip, and sort components write back through onChange
  // callbacks (Svelte 5 idiom — no on:/bind: required). Each handler
  // routes the new value through the corresponding setter on the store
  // so the cursor is reset and a fresh page is fetched.
  function onFiltersChange(next: SearchFilters): void {
    void s.setFilters(next);
  }
  function onSortChange(next: SearchSort): void {
    void s.setSort(next);
  }

  onMount(() => {
    const m = router.current;
    if (m.route !== "search") return;
    const initialQuery = m.q ?? "";
    const initialSort: SearchSort = m.sort ?? "relevance";
    // setSort/setFilters each trigger a fetch with cursor reset, so we
    // route the *initial* hydration through the store's setters in
    // order: filters → sort → query. Each call supersedes the prior
    // in-flight request via the store's inflight token, so only the
    // last one's response lands in the UI.
    void s.setFilters(filtersFromMatch(m));
    void s.setSort(initialSort);
    void s.setQuery(initialQuery);
  });

  // Wrap SearchResult rows into the Media shape VirtualGrid expects.
  // /search doesn't carry a thumb URL on the wire (the thumb endpoint
  // is keyed by media_id + thumb_version), so we synthesize the same
  // /api/v1/media/:id/thumb URL the rest of the app uses. timestamp
  // can be null on rows that have no EXIF date — we fall back to
  // imported_at so the cell sort/grouping is stable. aspect defaults
  // to 1 when width/height are missing.
  const months: Month[] = $derived.by((): Month[] => {
    const items: Media[] = s.results.map((r) => {
      const ts = r.timestamp ?? r.imported_at;
      const aspect = r.width != null && r.height != null && r.height > 0
        ? r.width / r.height
        : 1;
      return {
        id: r.media_id,
        timestamp: ts,
        aspect,
        thumbUrl: `/api/v1/media/${r.media_id}/thumb?v=${r.thumb_version}`,
        taken: new Date(ts),
        thumbVersion: r.thumb_version,
      };
    });
    if (items.length === 0) return [];
    return [{ key: "search-results", items }];
  });

  function onLoadMore(): void {
    void s.fetchNextPage();
  }
</script>

<div class="search-page">
  <div class="search-toolbar">
    <SearchFiltersPopover filters={s.filters} onChange={onFiltersChange} />
    <SearchSortSegment sort={s.sort} onChange={onSortChange} />
    <IndexingStatusPill completeness={s.embeddingCompleteness} />
  </div>
  <SearchFilterChips filters={s.filters} onChange={onFiltersChange} />
  <IndexingStatusBanner
    completeness={s.embeddingCompleteness}
    semanticUnavailable={s.semanticUnavailable}
    reason={s.semanticUnavailableReason}
    hasQuery={s.query !== ""}
  />
  {#if s.results.length > 0}
    <VirtualGrid
      {months}
      {onLoadMore}
      timelineChrome={false}
    />
  {:else if s.loading}
    <div class="search-status">Searching…</div>
  {:else if s.query !== ""}
    <div class="search-status" data-testid="search-empty-state">
      No results for &ldquo;{s.query}&rdquo;.
    </div>
  {:else}
    <div class="search-status" data-testid="search-idle-state">
      Type a query above to search your photos.
    </div>
  {/if}
</div>

<style>
  .search-page { padding: 12px 16px; }
  .search-toolbar {
    display: flex;
    gap: 12px;
    align-items: center;
    margin-bottom: 8px;
  }
  .search-status {
    padding: 24px 16px;
    color: var(--text-secondary);
    text-align: center;
  }
</style>

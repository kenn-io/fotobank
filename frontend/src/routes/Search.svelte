<!-- frontend/src/routes/Search.svelte
     V1: page shell for /search. Reads the route match into the search
     store on mount and on every router.current change so external URL
     mutations (e.g. AppHeader typing into a query while already on
     /search) re-hydrate the store. Writes the store's reactive state
     back into the URL on every change so the URL is the source of
     truth and a full reload preserves the user's view. The
     IndexingStatusPill and IndexingStatusBanner project the search
     response's embedding_completeness / semantic_unavailable_reason
     into the UI; an SSE handler invalidates the store's requestHash
     when the activator promotes a new generation. -->
<script lang="ts">
  import { createSearchStore, type SearchStore } from "../lib/search/searchStore.svelte";
  import { searchClient } from "../lib/search/client";
  import type { SearchClient } from "../lib/search/client";
  import type { SearchFilters, SearchSort, SearchScoreComponents } from "../lib/search/types";
  import SearchFiltersPopover from "../lib/search/SearchFiltersPopover.svelte";
  import SearchFilterChips from "../lib/search/SearchFilterChips.svelte";
  import SearchSortSegment from "../lib/search/SearchSortSegment.svelte";
  import IndexingStatusPill from "../lib/search/IndexingStatusPill.svelte";
  import IndexingStatusBanner from "../lib/search/IndexingStatusBanner.svelte";
  import DiagnosticsBadge from "../lib/search/DiagnosticsBadge.svelte";
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import type { Month, Media } from "../lib/media/mediaStore.svelte";
  import { router } from "../lib/router/router.svelte";
  import type { EventsStore } from "../lib/events/eventsStore.svelte";
  import { AIInspectionStore } from "../lib/ai/inspectionStore.svelte";
  import { api } from "../lib/api/client";
  import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";
  import { captureMainScrollY } from "../lib/lightbox/scrollRestore.svelte";
  import { selection } from "../lib/selection/selectionStore.svelte";

  // Tests inject a stub store via the optional `store` prop; production
  // callers omit it and the route constructs its own backed by the
  // singleton search client. searchClient comes from the same module
  // that hosts createSearchClient (default same-origin fetch). The
  // optional events prop wires the page into the SSE stream so
  // ai.embed.generation_activated invalidates the store's requestHash
  // (the activator just promoted a new generation, prior cursors are
  // stale). Tests omit it; isolated renders without an events bus
  // simply don't get the invalidation, which is harmless because
  // those tests don't drive a multi-request flow.
  // inspectionStore exposes the per-user "AI Inspection" toggle. When
  // on, every issued search includes explain=true; the page renders a
  // DiagnosticsBadge overlay on each cell. Tests inject a stubbed
  // store; production builds a real one, loads the persisted value
  // (silently ignoring failures), and reads `enabled` reactively.
  let { store, client = searchClient, events, inspectionStore = new AIInspectionStore(api) }: {
    store?: SearchStore;
    client?: SearchClient;
    events?: EventsStore;
    inspectionStore?: AIInspectionStore;
  } = $props();
  // svelte-ignore state_referenced_locally
  void inspectionStore.load().catch(() => {});

  // svelte-ignore state_referenced_locally
  const s: SearchStore = store ?? createSearchStore({
    client,
    explain: () => inspectionStore.enabled,
  });

  // hydrated guards the URL-sync $effect from firing during the initial
  // hydration cycle (when the hydration effect writes the URL's params
  // *into* the store). Without it, the effect would fire 3+ times
  // during hydration — once per setFilters/setSort/setQuery — and each
  // fire would navigate(replace) with a partial state that doesn't
  // reflect the user's URL until the last call lands. The effect skips
  // while hydrated=false so the first user-driven mutation produces
  // the first URL write.
  let hydrated = $state(false);

  // lastHydratedKey deduplicates hydration: the URL-sync $effect writes
  // back to the URL after each store mutation, which mutates
  // router.current and would otherwise re-fire the hydration effect
  // forever. Stringifying the relevant search-route fields and skipping
  // when the key matches the last hydration is the cheapest way to
  // break the loop without coupling the two effects.
  let lastHydratedKey = $state("");

  // lastSyncedKey is the dual: when the URL-sync $effect writes a URL,
  // it records the key it just wrote. The hydration effect, which
  // re-runs once the navigate() call mutates router.current, recognises
  // its own echo and skips the setter cascade. lastHydratedKey alone
  // wasn't sufficient because the URL-sync writeback runs AFTER the
  // hydration effect and can produce a key the hydration effect hasn't
  // observed yet (e.g. fresh mount → setters → URL writeback → router
  // change → hydration: the round-trip key is novel to the hydration
  // effect's lastHydratedKey state).
  let lastSyncedKey = $state("");

  // computeKey serialises the canonical search shape into a string the
  // two effects compare against. The shape mirrors what the URL would
  // encode: q, sort, date filters, tag labels (already strings, sorted
  // for stable order), location label, media_type, include_hidden.
  // Sort defaults to "relevance" on both sides so the empty-state
  // /search and a freshly-hydrated relevance store produce the same
  // key. Tag arrays are sorted because route → tag arrives in URL
  // order while store → tag is in user-add order; without sorting the
  // round-trip would diverge.
  function computeKey(shape: {
    q: string;
    sort: SearchSort;
    dateAfter?: string;
    dateBefore?: string;
    tagLabels: string[];
    location?: string;
    mediaType?: "photo" | "video";
    includeHidden: boolean;
  }): string {
    return JSON.stringify({
      q: shape.q,
      sort: shape.sort,
      date_after: shape.dateAfter ?? "",
      date_before: shape.dateBefore ?? "",
      tag: [...shape.tagLabels].sort(),
      location: shape.location ?? "",
      media_type: shape.mediaType ?? "",
      include_hidden: shape.includeHidden,
    });
  }

  // filtersFromMatch projects the route match into the SearchFilters
  // shape the store expects. It is only called inside the hydration
  // effect with a narrowed `match.route === "search"` pre-condition,
  // so the typed fields are safe to read directly. tags arrive on the
  // wire as tag_label strings (the ?tag= query repeats the label, not
  // a canonical key); without the canonical key we mirror label → key,
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
    if (m.include_hidden === true) out.includeHidden = true;
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

  // Hydration effect: re-runs whenever router.current changes (the
  // initial mount, a popstate from back/forward, or AppHeader writing a
  // new ?q=). Skips when the route is not "search", and dedupes by
  // serialised key so the URL-sync effect's writeback doesn't trigger
  // a second hydration. The setFilters → setSort → setQuery order
  // matches the original onMount; each call supersedes the prior
  // in-flight request via the store's inflight token, so only the last
  // one's response lands in the UI. The async fetch promises are
  // intentionally left unawaited — URL state and the request are
  // independent concerns and the URL must not block on the request.
  // We gate on inspectionStore.loaded so the first hydration sees the
  // persisted ai.inspection setting. Otherwise the explainGetter
  // reads its default (false) on the very first request and a user
  // with persisted ai.inspection=true gets a non-diagnostic page on
  // landing. Subsequent re-runs (e.g. AppHeader typing) don't depend
  // on this gate because loaded stays true.
  $effect(() => {
    if (!inspectionStore.loaded) return;
    const m = router.current;
    if (m.route !== "search") return;
    const key = computeKey({
      q: m.q ?? "",
      sort: m.sort ?? "relevance",
      ...(m.date_after !== undefined ? { dateAfter: m.date_after } : {}),
      ...(m.date_before !== undefined ? { dateBefore: m.date_before } : {}),
      tagLabels: m.tag ?? [],
      ...(m.location !== undefined ? { location: m.location } : {}),
      ...(m.media_type !== undefined ? { mediaType: m.media_type } : {}),
      includeHidden: m.include_hidden ?? false,
    });
    if (key === lastHydratedKey) return;
    if (key === lastSyncedKey) {
      // The URL-sync effect just wrote this key; the resulting
      // router.current change is our own echo. Record the hydration
      // marker so future runs short-circuit without re-issuing the
      // setter cascade, and bail.
      lastHydratedKey = key;
      return;
    }
    lastHydratedKey = key;
    const initialQuery = m.q ?? "";
    const initialSort: SearchSort = m.sort ?? "relevance";
    void s.setFilters(filtersFromMatch(m));
    void s.setSort(initialSort);
    void s.setQuery(initialQuery);
    hydrated = true;
  });

  // URL param sync: project the store's current state back into the
  // location bar so a reload, deep link, or share preserves the view.
  // Tag values are written as labels (the backend's ?tag= param is
  // matched against tag_label per the O1 contract; the canonical
  // tag_key only exists inside the store for dedupe). Sort omits the
  // "relevance" default so the canonical URL for an empty-state /search
  // is exactly /search with no query string.
  $effect(() => {
    if (!hydrated) return;
    const key = computeKey({
      q: s.query,
      sort: s.sort,
      ...(s.filters.dateAfter !== undefined && s.filters.dateAfter !== ""
        ? { dateAfter: s.filters.dateAfter }
        : {}),
      ...(s.filters.dateBefore !== undefined && s.filters.dateBefore !== ""
        ? { dateBefore: s.filters.dateBefore }
        : {}),
      tagLabels: s.filters.tags.map((t) => t.tag_label),
      ...(s.filters.location !== undefined && s.filters.location.location_label !== ""
        ? { location: s.filters.location.location_label }
        : {}),
      ...(s.filters.mediaType !== undefined ? { mediaType: s.filters.mediaType } : {}),
      includeHidden: s.filters.includeHidden === true,
    });
    if (key === lastSyncedKey) return;
    lastSyncedKey = key;
    const params = new URLSearchParams();
    if (s.query !== "") params.set("q", s.query);
    if (s.sort !== "relevance") params.set("sort", s.sort);
    if (s.filters.dateAfter !== undefined && s.filters.dateAfter !== "") {
      params.set("date_after", s.filters.dateAfter);
    }
    if (s.filters.dateBefore !== undefined && s.filters.dateBefore !== "") {
      params.set("date_before", s.filters.dateBefore);
    }
    for (const t of s.filters.tags) params.append("tag", t.tag_label);
    if (s.filters.location !== undefined && s.filters.location.location_label !== "") {
      params.set("location", s.filters.location.location_label);
    }
    if (s.filters.mediaType !== undefined) params.set("media_type", s.filters.mediaType);
    if (s.filters.includeHidden === true) params.set("include_hidden", "true");
    const qs = params.toString();
    const target = qs.length > 0 ? `/search?${qs}` : "/search";
    // replace: true so typing into a filter doesn't fill the back stack.
    // Compare against current path+search before navigating: navigate()
    // calls syncFromLocation() which mutates router.current; if the URL
    // is already what we'd write, skipping the navigate avoids
    // re-running every dependent reactive read.
    const current = window.location.pathname + window.location.search;
    if (current !== target) {
      router.navigate(target, { replace: true });
    }
  });

  // SSE invalidation: when the activator promotes a new embedding
  // generation (ai.embed.generation_activated), the prior cursor's
  // ReqHash no longer matches a fresh request. Calling
  // store.onGenerationActivated() clears the client-side requestHash
  // so the store treats subsequent results as a fresh page rather
  // than appending to a now-stale cursor. We deliberately do not
  // reissue the current query here — the user may have navigated
  // away or might not be expecting their results to change under
  // them; the next user-driven mutation will produce the fresh
  // request. lastEventId guards against double-firing when the
  // events store re-emits the same event (the stored lastEvent is
  // a single field and a re-render shouldn't trigger another call).
  let lastEmbedActivatedId = $state<string | null>(null);
  $effect(() => {
    const ev = events?.lastEvent;
    if (!ev) return;
    if (ev.type !== "ai.embed.generation_activated") return;
    if (ev.id === lastEmbedActivatedId) return;
    lastEmbedActivatedId = ev.id;
    s.onGenerationActivated();
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

  // scoreComponentsByMediaId is the per-cell lookup the cellOverlay
  // snippet consults to decide whether to mount a DiagnosticsBadge.
  // Built from s.results so a re-fetch (or a fresh request triggered
  // by a generation-activated SSE) replaces the map atomically — the
  // grid never reads a stale (id, scoreComponents) pairing across
  // requests. Only populated when explain=true was honoured by the
  // backend; absent rows render bare cells.
  const scoreComponentsByMediaId: Map<string, SearchScoreComponents> = $derived.by(() => {
    const m = new Map<string, SearchScoreComponents>();
    for (const r of s.results) {
      if (r.score_components) m.set(r.media_id, r.score_components);
    }
    return m;
  });

  function onLoadMore(): void {
    void s.fetchNextPage();
  }

  // openMedia captures the search-context source state into
  // lightboxSession before navigating to /media/:id?from=search.
  // navIds walks the current results in display order (selection
  // narrowing mirrors Library/Albums/Sessions when a multi-selection
  // covers the clicked id). scoreComponentsByMediaId travels with the
  // snapshot so LightboxMetadata can surface the Search relevance row
  // for whichever id is active in the lightbox.
  //
  // Search reconstruction isn't possible from the URL alone: the
  // result list depends on q/filters/explain that don't appear in
  // /media/:id. Direct entry to a /media/:id?from=search URL falls
  // through to the DirectMediaDetail fallback in Lightbox.svelte —
  // that's the explicit failure mode for the search reconstruction
  // path.
  function openMedia(id: string): void {
    const all = s.results.map((r) => r.media_id);
    const sel = selection.ids;
    const useSelection = sel.size > 1 && sel.has(id);
    const navIds = useSelection ? all.filter((x) => sel.has(x)) : all;
    lightboxSession.open({
      source: { kind: "search" },
      navIds,
      selected: useSelection,
      scrollY: captureMainScrollY(),
      returnFocusMediaId: id,
      returnHref: window.location.pathname + window.location.search,
      scoreComponentsById: scoreComponentsByMediaId,
    });
    router.navigate(`/media/${id}?from=search`);
  }
</script>

<div class="search-page">
  <div class="search-toolbar">
    <SearchFiltersPopover filters={s.filters} onChange={onFiltersChange} />
    <SearchSortSegment sort={s.sort} query={s.query} onChange={onSortChange} />
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
      onOpenMedia={openMedia}
    >
      {#snippet cellOverlay(m)}
        {@const comps = scoreComponentsByMediaId.get(m.id)}
        {#if comps}
          <DiagnosticsBadge components={comps} />
        {/if}
      {/snippet}
    </VirtualGrid>
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

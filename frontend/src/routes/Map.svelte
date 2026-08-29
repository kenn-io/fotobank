<!-- frontend/src/routes/Map.svelte
     Route component for /map. F3 mounts MapPane in a 60/40 split-view;
     F4 fills the right-side grid (.grid-side) with MapGridPane and
     wires marker/photo clicks through a shared lightbox handoff. The
     component receives geoStore as a prop so tests can construct the
     store with a fake typed-client without stubbing global fetch.
-->
<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import type L from "leaflet";
  import MapPane from "../lib/map/MapPane.svelte";
  import MapGridPane from "../lib/map/MapGridPane.svelte";
  import FilterChipStrip from "../lib/filters/FilterChipStrip.svelte";
  import { filterKey, type ActiveFilters } from "../lib/filters/activeFilters";
  import type { GeoStore } from "../lib/map/geoStore.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
  import { router } from "../lib/router/router.svelte";
  import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";

  // F3 consumes geoStore plus the route-derived params (z/c/focus). The
  // remaining props are typed up-front so F5-F9 can wire them in
  // without changing the call site in App.svelte. The route params use
  // `T | undefined` (not `?:`) because exactOptionalPropertyTypes:true
  // rejects assigning `undefined` to a `?` optional, and the router can
  // supply undefined.
  //
  // SF-19 added activeFilters / tagLabels / onFiltersChange so the
  // FilterChipStrip can mount above the map and chip toggles re-fetch
  // /api/v1/media/geo with the narrowed param set. has_gps is
  // intentionally NOT forwarded — /geo's contract is geotagged-only.
  let {
    z,
    c,
    focus,
    tab,
    geoStore,
    mediaStore,
    hiddenStore,
    toastStore,
    activeFilters,
    tagLabels,
    onFiltersChange,
  }: {
    z: number | undefined;
    c: [number, number] | undefined;
    focus: string | undefined;
    tab: "map" | "photos" | undefined;
    geoStore: GeoStore;
    mediaStore: MediaStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
    activeFilters: ActiveFilters;
    tagLabels: Record<string, string>;
    onFiltersChange: (next: ActiveFilters) => void;
  } = $props();

  // viewportIds is populated by MapPane's onViewportChange callback;
  // clusterIds is set by F5 when the user clicks a cluster (today it's
  // only cleared by MapPane.onClearClusterFilter and MapGridPane's
  // clear-chip). MapGridPane reads clusterIds-or-viewportIds to drive
  // the grid.
  let viewportIds = $state<string[]>([]);
  let clusterIds = $state<string[] | null>(null);

  // zState/cState track the latest view state emitted by MapPane.
  // currentMapReturnHref reads these (not the original z/c props) so
  // the lightbox snapshot's returnHref reflects whatever the user is
  // looking at the moment they click a photo, not the route's initial
  // params. The URL writer below debounces history.replaceState by
  // 300ms so a mouse-drag pan doesn't churn the history.
  //
  // The seed below captures the route's initial z/c only — that's
  // intentional. After mount, the values come from MapPane's
  // onViewState callback. The route remounts on any /map → /map
  // navigation that goes through router.navigate, so the seed is
  // always re-read from the latest URL on entry.
  // svelte-ignore state_referenced_locally
  let zState = $state<number | undefined>(z);
  // svelte-ignore state_referenced_locally
  let cState = $state<[number, number] | undefined>(c);
  let writeTimer: ReturnType<typeof setTimeout> | undefined = undefined;

  function onMapViewState(state: { z: number; c: [number, number] }): void {
    zState = state.z;
    cState = state.c;
    if (writeTimer !== undefined) clearTimeout(writeTimer);
    writeTimer = setTimeout(() => {
      // Preserve every existing query param except z/c/focus/tab so the
      // sidebar facet selection (camera/lens/facet_tag/has_gps/media_type)
      // survives a pan-zoom. Without this the user-facing chips stay in
      // ActiveFilters but the URL gets rewritten to bare ?z&c, and a
      // page refresh would drop the filter set. Tab is also stripped so
      // a /map?tab=photos URL the user is actively viewing isn't churned
      // by the live map view-state writer (the tab state lives in
      // activeTab, not the URL — re-emitting it would no-op anyway).
      const sp = new URLSearchParams(window.location.search);
      sp.delete("z");
      sp.delete("c");
      sp.delete("focus");
      sp.delete("tab");
      sp.set("z", String(state.z));
      sp.set("c", `${state.c[0]},${state.c[1]}`);
      // Preserve focus if the URL had it — F7 wires the focus prop
      // into the initial-load path; we keep the param so a refresh
      // re-runs the same retry behavior.
      if (focus !== undefined) sp.set("focus", focus);
      history.replaceState({}, "", `/map?${sp.toString()}`);
    }, 300);
  }

  // openMedia is the shared lightbox-handoff entry point. Both the
  // Leaflet marker click and the MapGridPane photo click route through
  // it so the lightbox always sees the same snapshot regardless of
  // which surface launched it. The active id list mirrors what the
  // grid is rendering — clusterIds when filtered, viewportIds
  // otherwise — so prev/next inside the lightbox walks the same set
  // the user just saw.
  function openMedia(id: string): void {
    const orderedIds = clusterIds !== null ? clusterIds : viewportIds;
    lightboxSession.open({
      source: { kind: "map" },
      navIds: [...orderedIds],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: id,
      returnHref: currentMapReturnHref(),
      includeHidden: geoStore.includedHiddenAtFetch,
    });
    router.navigate(`/media/${id}?from=map`);
  }

  // currentMapReturnHref preserves the latest zoom/center on the
  // snapshot's returnHref so the lightbox's back/close path lands on
  // the same map view the user launched from. zState/cState are
  // refreshed on every Leaflet moveend/zoomend (via onMapViewState)
  // and seeded from the initial route params, so this works whether
  // the user just landed on /map or has been panning around.
  //
  // SF-19: also preserve the active filter params (camera/lens/
  // facet_tag/has_gps/media_type) by reading window.location.search
  // and stripping the ones the writer owns (z/c/focus/tab). Without
  // this a user on /map?camera=Sony who clicks a photo and closes the
  // lightbox would land on /map?z=…&c=… with the camera chip dropped.
  function currentMapReturnHref(): string {
    const sp = new URLSearchParams(window.location.search);
    sp.delete("z");
    sp.delete("c");
    sp.delete("focus");
    sp.delete("tab");
    if (zState !== undefined) sp.set("z", String(zState));
    if (cState !== undefined) sp.set("c", `${cState[0]},${cState[1]}`);
    const q = sp.toString();
    return q ? `/map?${q}` : "/map";
  }

  function onClusterClick(ids: string[], _bounds: L.LatLngBounds): void {
    // Cluster click filters the right-grid to the cluster's children.
    // Leaflet's default cluster behavior also zooms in; we let that run
    // and only update the grid filter here. The user clears the filter
    // either via the grid's "× Clear filter" chip or by clicking
    // empty map space (MapPane wires that to onClearClusterFilter).
    clusterIds = ids;
  }

  // includeHiddenToggle is the source of truth for whether the geo set
  // includes hidden rows. F8 will surface this as a header checkbox;
  // F7 only flips it on automatically when ?focus=<id> targets a hidden
  // photo and the user has unlocked hidden. The toggle is also read by
  // currentMapReturnHref → snapshot.includeHidden so the lightbox knows
  // the navIds may include hidden rows.
  let includeHiddenToggle = $state(false);

  // F9 mobile tabs. activeTab seeded from the route's `?tab=` param
  // (lets the user share a /map?tab=photos URL); defaults to "map" so
  // first-load on mobile shows the map. mapPaneEl exposes
  // invalidateSize(), which Leaflet needs after a hidden→visible
  // transition (the map caches its container size at mount and renders
  // a clipped tile grid otherwise).
  // svelte-ignore state_referenced_locally
  let activeTab = $state<"map" | "photos">(tab ?? "map");
  let mapPaneEl: { invalidateSize: () => void } | null = $state(null);

  function setTab(next: "map" | "photos"): void {
    activeTab = next;
    // requestAnimationFrame waits for the next paint so the now-visible
    // .map-side has its real width. Calling invalidateSize() synchronously
    // would still see the old (display:none) bounding box.
    if (next === "map") {
      requestAnimationFrame(() => mapPaneEl?.invalidateSize());
    }
  }

  onMount(() => {
    void initialLoad();
  });

  // loadAndMerge centralizes the load+merge sequence so initialLoad,
  // toggle, and retry all behave the same way. mergeRaw drops hidden
  // rows by design (MediaStore is the visible-only index) — hidden geo
  // rows still surface in MapGridPane via the geoStore fallback chain.
  //
  // SF-19: forward the four facet param groups from activeFilters into
  // geoStore.load. has_gps is intentionally excluded — /geo's contract
  // is geotagged-only, so passing it would be a wasted round-trip at
  // best and a contract mismatch at worst.
  async function loadAndMerge(includeHidden: boolean): Promise<void> {
    await geoStore.load(includeHidden, {
      cameras: activeFilters.cameras,
      lenses: activeFilters.lenses,
      facetTags: activeFilters.tagKeys,
      mediaType: activeFilters.mediaType,
    });
    mediaStore.mergeRaw(geoStore.rawItems);
  }

  // initialLoad runs the visible-only fetch first; if ?focus=<id> isn't
  // present in the result and the user has unlocked hidden, retry with
  // include_hidden=true. The retry only fires once per mount — if the
  // photo still isn't there, we toast and stop.
  async function initialLoad(): Promise<void> {
    await loadAndMerge(false);
    if (
      focus !== undefined
      && geoStore.findById(focus) === undefined
      && hiddenStore.unlocked
    ) {
      includeHiddenToggle = true;
      await loadAndMerge(true);
      if (geoStore.findById(focus) === undefined) {
        toastStore.push({ kind: "info", message: "Photo not found on map." });
      }
    }
  }

  // Drop the in-flight URL writer if the route unmounts mid-debounce
  // — replaceState() on the next route's pathname would corrupt history.
  onDestroy(() => {
    if (writeTimer !== undefined) clearTimeout(writeTimer);
  });

  // The toggle is session-only — never persisted to the URL. Spec choice:
  // "Include hidden" is a privacy-sensitive view; surfacing it in a
  // shareable link risks leaking the toggle state into bookmarks.
  async function onToggleHidden(next: boolean): Promise<void> {
    includeHiddenToggle = next;
    await loadAndMerge(next);
  }

  // Lock-state cleanup. If the hidden session locks (manual lock, idle
  // timeout, page-hide auto-lock) while hidden rows are loaded into the
  // map, those markers would otherwise stay visible until refresh. The
  // effect drops the toggle and reloads visible-only as soon as the
  // unlocked transition flips false. Reading both reactive sources is
  // intentional — Svelte tracks the dependency and re-fires on either.
  $effect(() => {
    if (!hiddenStore.unlocked && includeHiddenToggle) {
      includeHiddenToggle = false;
      void loadAndMerge(false);
    }
  });

  // Retry handler for the error branch. Defensive against a 403 from an
  // expired hidden-unlock cookie that the SPA still believes is alive
  // (the lock-cleanup $effect only fires on the SPA's hiddenStore.unlocked
  // transition, not on a server-side cookie expiry mid-session). Without
  // this guard, retrying with includeHidden=true would loop forever
  // against the same 403. Drop the toggle on retry so we always start
  // from the visible-only baseline.
  function onRetryLoad(): void {
    includeHiddenToggle = false;
    void loadAndMerge(false);
  }

  // SF-19: re-fetch the geo set when the active filters change. We
  // gate on `lastFilterKey !== currentKey` so the effect does NOT
  // double-fire on mount: initialLoad runs from onMount and seeds
  // lastFilterKey on entry, so by the time this effect first runs the
  // keys match and the body is a no-op. Subsequent filter toggles
  // (chip strip clicks, sidebar facet clicks via App.onFiltersChange)
  // change the URL → activeFilters changes → this effect runs once
  // per change. lastFilterKey is captured eagerly with
  // state_referenced_locally so the seed reflects the route's initial
  // filter set, not "" (which would always trigger one unwanted run).
  // svelte-ignore state_referenced_locally
  let lastFilterKey = $state<string>(filterKey(activeFilters));
  $effect(() => {
    const currentKey = filterKey(activeFilters);
    if (currentKey === lastFilterKey) return;
    lastFilterKey = currentKey;
    // Clear the cluster filter before re-fetching: a cluster selection
    // is a slice of the prior result set (the IDs the user clicked into),
    // and that slice's identity is invalidated the moment the filter set
    // changes. Without this, MapGridPane would keep using the stale
    // clusterIds (which it prefers over viewportIds) and render rows
    // from the previous filter set even though the map markers now
    // reflect the new one.
    clusterIds = null;
    void loadAndMerge(includeHiddenToggle);
  });
</script>

<section class="map-page" data-testid="map-page">
  <!-- SF-19: chip strip mounts above the rest of the page so filter
       state is always visible regardless of which load-state branch is
       rendering (loading / error / empty / loaded). The component
       returns an empty fragment when activeFilters is empty, so a user
       on /map with no chips active sees no extra chrome. -->
  <FilterChipStrip filters={activeFilters} {tagLabels} onChange={onFiltersChange} />

  <!-- Header is rendered for every state branch (loading/error/empty/loaded)
       once unlocked, so an unlocked user with zero visible geotagged
       photos can still flip on Include hidden to reveal hidden-only
       geotagged rows. -->
  {#if hiddenStore.unlocked || geoStore.items.length > 0}
    <header class="map-page-header">
      <nav class="tabs" aria-label="Map view">
        <button
          type="button"
          class:active={activeTab === "map"}
          onclick={() => setTab("map")}
        >Map</button>
        <button
          type="button"
          class:active={activeTab === "photos"}
          onclick={() => setTab("photos")}
        >Photos</button>
      </nav>
      {#if hiddenStore.unlocked}
        <label class="hidden-toggle">
          <input
            type="checkbox"
            checked={includeHiddenToggle}
            onchange={(e) => onToggleHidden(e.currentTarget.checked)}
          />
          Include hidden
        </label>
      {/if}
    </header>
  {/if}

  {#if !geoStore.ready && geoStore.error === null}
    <div class="loading">Loading your photo locations…</div>
  {:else if geoStore.error !== null}
    <div class="error">
      Couldn't load photo locations.
      <button onclick={onRetryLoad}>Retry</button>
    </div>
  {:else if geoStore.items.length === 0}
    <div class="empty">
      No geotagged photos in your library yet. Photos with GPS metadata will appear here as you import.
    </div>
  {:else}
    <div
      class="map-page-grid"
      data-testid="map-loaded"
      data-active={activeTab}
    >
      <div class="map-side">
        <MapPane
          bind:this={mapPaneEl}
          items={geoStore.items}
          initialZoom={z}
          initialCenter={c}
          focusId={focus}
          onMarkerClick={(id) => openMedia(id)}
          onClusterClick={(ids, bounds) => onClusterClick(ids, bounds)}
          onViewportChange={(ids) => (viewportIds = ids)}
          onViewState={onMapViewState}
          onClearClusterFilter={() => (clusterIds = null)}
        />
      </div>
      <div class="grid-side">
        <MapGridPane
          visibleIds={viewportIds}
          {clusterIds}
          {mediaStore}
          {geoStore}
          onPhotoClick={(id) => openMedia(id)}
          onClearClusterFilter={() => (clusterIds = null)}
        />
      </div>
    </div>
  {/if}
</section>

<style>
  .map-page {
    display: flex;
    flex-direction: column;
    height: calc(100vh - var(--header-height, 56px));
  }
  .loading,
  .error,
  .empty {
    padding: 24px;
    color: var(--text-secondary);
  }
  .map-page-header {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-default);
    flex: 0 0 auto;
  }
  .hidden-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--text-secondary);
    cursor: pointer;
    margin-left: auto;
  }
  .tabs {
    display: none;
    gap: 4px;
  }
  .tabs button {
    background: transparent;
    border: 1px solid var(--border-default);
    border-radius: 12px;
    padding: 4px 12px;
    font-size: 13px;
    cursor: pointer;
    color: var(--text-secondary);
  }
  .tabs button.active {
    background: var(--accent-blue);
    border-color: var(--accent-blue);
    /* Dark foreground on the amber pill — pairing the light --text-primary
       body color with warm-orange amber gave a low-contrast active
       label. --bg-primary (canvas dark) restores readable contrast. */
    color: var(--bg-primary);
  }
  .map-page-grid {
    display: grid;
    grid-template-columns: 60% 40%;
    flex: 1 1 auto;
    min-height: 0;
  }
  .map-side,
  .grid-side {
    height: 100%;
    overflow: hidden;
  }
  /* Split view requires the main pane (viewport minus the 220px sidebar)
     to be wide enough that 60% leaves enough room for the map AND 40%
     leaves enough room for ~3 grid columns. Below 1240px the main pane
     is < 1020px and 40% of that fits only one and a half thumbnails;
     fall back to tabs. The sidebar doesn't collapse on mobile, so a
     viewport-relative breakpoint here matches actual main-pane width. */
  @media (max-width: 1239px) {
    .tabs {
      display: flex;
    }
    .map-page-grid {
      grid-template-columns: 1fr;
    }
    .map-page-grid[data-active="map"] .grid-side {
      display: none;
    }
    .map-page-grid[data-active="photos"] .map-side {
      display: none;
    }
  }
</style>

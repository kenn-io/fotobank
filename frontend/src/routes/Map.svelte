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
  let {
    z,
    c,
    focus,
    geoStore,
    mediaStore,
  }: {
    z: number | undefined;
    c: [number, number] | undefined;
    focus: string | undefined;
    tab: "map" | "photos" | undefined;
    geoStore: GeoStore;
    mediaStore: MediaStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
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
      const sp = new URLSearchParams();
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
  function currentMapReturnHref(): string {
    const sp = new URLSearchParams();
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

  onMount(() => {
    void geoStore.load(false);
  });

  // Drop the in-flight URL writer if the route unmounts mid-debounce
  // — replaceState() on the next route's pathname would corrupt history.
  onDestroy(() => {
    if (writeTimer !== undefined) clearTimeout(writeTimer);
  });
</script>

<section class="map-page" data-testid="map-page">
  {#if !geoStore.ready && geoStore.error === null}
    <div class="loading">Loading your photo locations…</div>
  {:else if geoStore.error !== null}
    <div class="error">
      Couldn't load photo locations.
      <button onclick={() => geoStore.load(false)}>Retry</button>
    </div>
  {:else if geoStore.items.length === 0}
    <div class="empty">
      No geotagged photos in your library yet. Photos with GPS metadata will appear here as you import.
    </div>
  {:else}
    <div class="map-page-grid" data-testid="map-loaded">
      <div class="map-side">
        <MapPane
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
          onPhotoClick={(id) => openMedia(id)}
          onClearClusterFilter={() => (clusterIds = null)}
        />
      </div>
    </div>
  {/if}
</section>

<style>
  .map-page {
    display: block;
    height: calc(100vh - var(--header-height, 56px));
  }
  .loading,
  .error,
  .empty {
    padding: 24px;
    color: var(--text-secondary, #6b7280);
  }
  .map-page-grid {
    display: grid;
    grid-template-columns: 60% 40%;
    height: 100%;
  }
  .map-side,
  .grid-side {
    height: 100%;
    overflow: hidden;
  }
  @media (max-width: 1023px) {
    .map-page-grid {
      grid-template-columns: 1fr;
    }
    .grid-side {
      display: none;
    }
  }
</style>

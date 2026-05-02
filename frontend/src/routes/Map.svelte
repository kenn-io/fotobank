<!-- frontend/src/routes/Map.svelte
     Route component for /map. F3 mounts MapPane in a 60/40 split-view;
     F4 will fill the right-side grid (.grid-side). The component
     receives geoStore as a prop so tests can construct the store with
     a fake typed-client without stubbing global fetch.
-->
<script lang="ts">
  import { onMount } from "svelte";
  import type L from "leaflet";
  import MapPane from "../lib/map/MapPane.svelte";
  import type { GeoStore } from "../lib/map/geoStore.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";

  // F3 consumes geoStore plus the route-derived params (z/c/focus). The
  // remaining props are typed up-front so F4-F9 can wire them in
  // without changing the call site in App.svelte. The route params use
  // `T | undefined` (not `?:`) because exactOptionalPropertyTypes:true
  // rejects assigning `undefined` to a `?` optional, and the router can
  // supply undefined.
  let {
    z,
    c,
    focus,
    geoStore,
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

  // viewportIds and clusterIds are populated by MapPane callbacks.
  // F4 reads viewportIds to filter the right-grid; F5 reads clusterIds
  // to override the filter when a cluster is clicked. Today they're
  // declared so the MapPane callbacks have somewhere to write.
  let viewportIds = $state<string[]>([]);
  let clusterIds = $state<string[] | null>(null);

  // F4 will navigate to /m/:id with from=map; F5 will set clusterIds
  // and the right-grid will filter to those rows. Today these are
  // logging stubs so the props have a concrete handler shape to bind to.
  function onMarkerClick(id: string): void {
    console.debug("[map] marker click", id);
  }
  function onClusterClick(ids: string[], bounds: L.LatLngBounds): void {
    console.debug("[map] cluster click", ids.length, bounds);
  }

  onMount(() => {
    void geoStore.load(false);
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
          onMarkerClick={(id) => onMarkerClick(id)}
          onClusterClick={(ids, bounds) => onClusterClick(ids, bounds)}
          onViewportChange={(ids) => (viewportIds = ids)}
          onClearClusterFilter={() => (clusterIds = null)}
        />
      </div>
      <div class="grid-side">
        <!-- Filled in F4 -->
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

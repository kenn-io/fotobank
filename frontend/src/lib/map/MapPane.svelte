<!-- frontend/src/lib/map/MapPane.svelte
     Leaflet map with marker clustering. Owns the map instance, the
     cluster group, and the id↔marker bookkeeping; emits viewport-visible
     ids upward on every moveend/zoomend so the right-side grid can
     filter to what's on screen.

     The cluster reverse-lookup uses a parallel marker→id map (instead of
     iterating markersById on every cluster click) so an N-marker cluster
     resolves in O(N) instead of O(N²). buildMarkers keeps both maps in
     lockstep; clearLayers() preserves them only because we clear them
     ourselves immediately after.
-->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import L from "leaflet";
  import "leaflet/dist/leaflet.css";
  import "leaflet.markercluster";
  import "leaflet.markercluster/dist/MarkerCluster.css";
  import "leaflet.markercluster/dist/MarkerCluster.Default.css";
  import { tileUrl, attribution, defaultMaxZoom } from "./tiles";
  import type { Media } from "../media/mediaStore.svelte";

  // exactOptionalPropertyTypes:true rejects `prop?: T` when the parent
  // passes an explicit `undefined`, so the route-derived params use
  // `T | undefined`. Matches the convention in Map.svelte.
  let {
    items,
    initialZoom,
    initialCenter,
    focusId,
    onMarkerClick,
    onClusterClick,
    onViewportChange,
    onViewState,
    onClearClusterFilter,
  }: {
    items: Media[];
    initialZoom: number | undefined;
    initialCenter: [number, number] | undefined;
    focusId: string | undefined;
    onMarkerClick: (id: string) => void;
    onClusterClick: (ids: string[], bounds: L.LatLngBounds) => void;
    onViewportChange: (visibleIds: string[]) => void;
    onViewState: (state: { z: number; c: [number, number] }) => void;
    onClearClusterFilter: () => void;
  } = $props();

  let container: HTMLDivElement | null = $state(null);
  let map: L.Map | null = null;
  let cluster: L.MarkerClusterGroup | null = null;
  // Leaflet's getBounds()/getCenter() throw "Set map center and zoom
  // first" if no view has been applied. Track view-applied locally so
  // the $effect path (which can race onMount on first mount, and which
  // must remain a no-op when items is empty and fitToAll bails) skips
  // emitting in that window.
  let viewReady = false;
  const markersById = new Map<string, L.Marker>();
  const idByMarker = new Map<L.Marker, string>();

  function emitViewportVisible(): void {
    // Skip until the map has a view: getBounds() throws otherwise, and
    // we have nothing meaningful to report when there are no markers
    // and no caller-supplied center.
    if (map === null || !viewReady) return;
    const bounds = map.getBounds();
    const visible: string[] = [];
    for (const [id, marker] of markersById) {
      if (bounds.contains(marker.getLatLng())) visible.push(id);
    }
    onViewportChange(visible);
  }

  // Single moveend/zoomend handler so onViewportChange and onViewState
  // share one Leaflet event subscription. emitViewState skips while
  // viewReady is false because getCenter()/getZoom() throw before a
  // view is applied.
  function onMoveOrZoom(): void {
    emitViewportVisible();
    if (map === null || !viewReady) return;
    const center = map.getCenter();
    onViewState({ z: map.getZoom(), c: [center.lat, center.lng] });
  }

  function buildMarkers(): void {
    if (cluster === null) return;
    cluster.clearLayers();
    markersById.clear();
    idByMarker.clear();
    for (const m of items) {
      if (m.latitude == null || m.longitude == null) continue;
      const marker = L.marker([m.latitude, m.longitude]);
      marker.on("click", () => onMarkerClick(m.id));
      cluster.addLayer(marker);
      markersById.set(m.id, marker);
      idByMarker.set(marker, m.id);
    }
  }

  function fitToAll(): void {
    if (map === null || markersById.size === 0) return;
    const latLngs = [...markersById.values()].map((m) => m.getLatLng());
    map.fitBounds(L.latLngBounds(latLngs), { animate: false, padding: [40, 40] });
    viewReady = true;
  }

  onMount(() => {
    if (container === null) return;
    map = L.map(container, {
      zoomControl: true,
      attributionControl: true,
    });

    L.tileLayer(tileUrl(), {
      attribution: attribution(),
      maxZoom: defaultMaxZoom,
    }).addTo(map);

    cluster = L.markerClusterGroup();
    cluster.on("clusterclick", (e) => {
      const ids = e.layer
        .getAllChildMarkers()
        .map((mk: L.Marker) => idByMarker.get(mk) ?? null)
        .filter((x: string | null): x is string => x !== null);
      onClusterClick(ids, e.layer.getBounds());
      // Default markercluster behavior also zooms — we keep it.
    });
    map.addLayer(cluster);

    map.on("moveend zoomend", onMoveOrZoom);
    map.on("click", () => onClearClusterFilter());

    buildMarkers();

    // Initial view priority (per spec §3.1):
    //   1. focusId       → zoom 14 at that photo
    //   2. z + c         → use those
    //   3. otherwise     → fitBounds to all markers
    if (focusId !== undefined) {
      const found = items.find((m) => m.id === focusId);
      if (found && found.latitude != null && found.longitude != null) {
        map.setView([found.latitude, found.longitude], initialZoom ?? 14, { animate: false });
        viewReady = true;
      } else if (initialCenter !== undefined && initialZoom !== undefined) {
        map.setView(initialCenter, initialZoom, { animate: false });
        viewReady = true;
      } else {
        fitToAll();
      }
    } else if (initialCenter !== undefined && initialZoom !== undefined) {
      map.setView(initialCenter, initialZoom, { animate: false });
      viewReady = true;
    } else {
      fitToAll();
    }

    emitViewportVisible();
  });

  // Re-render markers whenever items change (e.g. include-hidden retry).
  // The `void items` read registers the dependency so $effect tracks
  // the prop reactively.
  $effect(() => {
    void items;
    buildMarkers();
    emitViewportVisible();
  });

  onDestroy(() => {
    if (map !== null) map.remove();
    map = null;
    cluster = null;
    markersById.clear();
    idByMarker.clear();
  });

  // Exposed to bind:this callers (F9 invalidates on mobile tab-switch
  // because Leaflet caches the container's offsetWidth at mount time
  // and otherwise renders a clipped tile grid after a hidden→visible
  // transition).
  export function invalidateSize(): void {
    if (map !== null) map.invalidateSize();
  }
</script>

<div bind:this={container} class="map-pane" data-testid="map-pane"></div>

<style>
  .map-pane {
    width: 100%;
    height: 100%;
  }
  /* Leaflet markers fall outside the Svelte tree, so theme them at :root. */
  :global(.leaflet-marker-icon.fb-marker-default) {
    background-color: var(--accent);
  }
</style>

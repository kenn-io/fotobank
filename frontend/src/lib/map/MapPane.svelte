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

     Marker icons are custom DivIcons (not L.marker's default PNGs):
     Leaflet's bundled marker-icon.png/marker-shadow.png URLs don't
     resolve under Vite — they 404 and render as broken-image glyphs.
     The amber dot below sidesteps that entirely. Cluster click avoids
     spiderfyOnMaxZoom (which would emit a radial spray of broken
     icons) — instead we either zoom the cluster apart or open a popup
     gallery anchored at the cluster.
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
  // focusApplied tracks whether the focusId centering has been applied
  // to the map view. Onmount sets it true if the focus item was found
  // in the initial items; otherwise the items-change effect retries
  // when items update (the F7 hidden-retry sequence loads visible-only
  // first, then re-loads with include_hidden — focusId may not be in
  // items until the second load lands).
  let focusApplied = false;
  const markersById = new Map<string, L.Marker>();
  const idByMarker = new Map<L.Marker, string>();
  // itemsById is rebuilt with markers so the cluster popup gallery can
  // pull thumbVersion / id without re-walking `items` per render. Keys
  // match markersById so a cluster's child markers map cleanly to rows.
  const itemsById = new Map<string, Media>();

  // Single shared DivIcon for every photo marker. Re-using one icon
  // instance is the leaflet idiom — DivIcons are stateless and the DOM
  // node is cloned per marker.
  const photoMarkerIcon = L.divIcon({
    className: "map-photo-pin-wrap",
    html: '<span class="map-photo-pin" aria-hidden="true"></span>',
    iconSize: [12, 12],
    iconAnchor: [6, 6],
  });

  function clusterIconHtml(count: number): L.DivIcon {
    const size = count < 10 ? 28 : count < 50 ? 36 : 44;
    return L.divIcon({
      className: "map-cluster-pin-wrap",
      html: `<div class="map-cluster-pin" style="width:${size}px;height:${size}px"><span>${count}</span></div>`,
      iconSize: [size, size],
      iconAnchor: [size / 2, size / 2],
    });
  }

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
    // An open cluster popup references markers by id from the
    // pre-rebuild cluster; once we clearLayers() those markers are
    // detached and the popup's grid cells no longer reflect any
    // current cluster. Close it first so a stale popup doesn't linger
    // through a filter change or hidden-include retry.
    map?.closePopup();
    cluster.clearLayers();
    markersById.clear();
    idByMarker.clear();
    itemsById.clear();
    for (const m of items) {
      if (m.latitude == null || m.longitude == null) continue;
      const marker = L.marker([m.latitude, m.longitude], { icon: photoMarkerIcon });
      marker.on("click", () => onMarkerClick(m.id));
      cluster.addLayer(marker);
      markersById.set(m.id, marker);
      idByMarker.set(marker, m.id);
      itemsById.set(m.id, m);
    }
  }

  function fitToAll(): void {
    if (map === null || markersById.size === 0) return;
    const latLngs = [...markersById.values()].map((m) => m.getLatLng());
    map.fitBounds(L.latLngBounds(latLngs), { animate: false, padding: [40, 40] });
    viewReady = true;
  }

  // Build the popup body imperatively (plain DOM, not a Svelte child).
  // Mounting a Svelte component into Leaflet's popup container fights
  // both lifecycles — Leaflet recycles the DOM on close, Svelte's
  // teardown is asynchronous, and the result is leaked listeners or
  // double-mounted state. A single delegated click handler resolves
  // each thumb's data-id back to onMarkerClick(id).
  function buildPopupContent(ids: string[]): HTMLElement {
    const root = document.createElement("div");
    root.className = "map-cluster-popup";

    const header = document.createElement("div");
    header.className = "map-cluster-popup__header";
    header.textContent = `[ ${ids.length} photo${ids.length === 1 ? "" : "s"} ]`;
    root.appendChild(header);

    const grid = document.createElement("div");
    grid.className = "map-cluster-popup__grid";
    for (const id of ids) {
      const m = itemsById.get(id);
      const v = m?.thumbVersion ?? 0;
      const url = `/api/v1/media/${encodeURIComponent(id)}/thumb?size=grid&v=${v}`;
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "map-cluster-popup__cell";
      cell.dataset["id"] = id;
      cell.style.backgroundImage = `url(${url})`;
      cell.setAttribute("aria-label", "Open photo");
      grid.appendChild(cell);
    }
    root.appendChild(grid);

    const footer = document.createElement("button");
    footer.type = "button";
    footer.className = "map-cluster-popup__footer";
    footer.textContent = "└ view all in grid →";
    root.appendChild(footer);

    root.addEventListener("click", (ev) => {
      const target = ev.target;
      if (!(target instanceof HTMLElement)) return;
      const cell = target.closest<HTMLElement>(".map-cluster-popup__cell");
      if (cell !== null) {
        const id = cell.dataset["id"];
        if (id !== undefined) onMarkerClick(id);
        return;
      }
      if (target.closest(".map-cluster-popup__footer") !== null) {
        if (map !== null) map.closePopup();
      }
    });

    return root;
  }

  function openClusterPopup(latlng: L.LatLng, ids: string[]): void {
    if (map === null) return;
    L.popup({
      maxWidth: 280,
      minWidth: 240,
      maxHeight: 320,
      closeButton: true,
      autoPan: true,
      className: "map-cluster-popup-shell",
    })
      .setLatLng(latlng)
      .setContent(buildPopupContent(ids))
      .openOn(map);
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

    cluster = L.markerClusterGroup({
      // Spiderfy + Leaflet's default cluster-zoom would either shoot
      // broken default icons radially or zoom-and-leave; we replace
      // both behaviors below with explicit zoom-or-popup branching.
      spiderfyOnMaxZoom: false,
      spiderfyOnEveryZoom: false,
      showCoverageOnHover: false,
      zoomToBoundsOnClick: false,
      iconCreateFunction: (c) => clusterIconHtml(c.getChildCount()),
    });
    cluster.on("clusterclick", (e) => {
      if (map === null) return;
      const childMarkers = e.layer.getAllChildMarkers();
      const ids = childMarkers
        .map((mk: L.Marker) => idByMarker.get(mk) ?? null)
        .filter((x: string | null): x is string => x !== null);
      const bounds = e.layer.getBounds();
      // Always notify the parent so the right-grid filters to the
      // cluster set, regardless of which branch (zoom/popup) we take.
      onClusterClick(ids, bounds);

      const targetZoom = map.getBoundsZoom(bounds, true);
      if (targetZoom > map.getZoom() + 0.5) {
        // Zoom-to-fit will split the cluster on the next render.
        map.fitBounds(bounds, { padding: [40, 40] });
      } else {
        // Cluster won't split (overlapping points or near-max zoom):
        // open the popup gallery anchored at the cluster center.
        openClusterPopup(e.layer.getLatLng(), ids);
      }
    });
    map.addLayer(cluster);

    map.on("moveend zoomend", onMoveOrZoom);
    map.on("click", () => onClearClusterFilter());

    buildMarkers();

    // Initial view priority (per spec §3.1):
    //   1. focusId       → zoom 14 at that photo (regardless of z=)
    //   2. z + c         → use those
    //   3. otherwise     → fitBounds to all markers
    // If focusId is set but not yet in items (F7 hidden-retry sequence
    // hasn't completed), fall through to z+c or fitToAll and let the
    // items-change effect re-apply focus when the row arrives.
    if (focusId !== undefined && tryApplyFocus()) {
      // applied via tryApplyFocus
    } else if (initialCenter !== undefined && initialZoom !== undefined) {
      map.setView(initialCenter, initialZoom, { animate: false });
      viewReady = true;
    } else {
      fitToAll();
    }

    emitViewportVisible();
  });

  // tryApplyFocus centers the map on focusId at zoom 14 if the row is
  // present and has GPS. Returns true on success so callers (onMount,
  // items-change effect) know whether to fall through to other branches.
  // Marks focusApplied so subsequent items changes don't snap the
  // viewport back if the user has panned away.
  function tryApplyFocus(): boolean {
    if (map === null || focusId === undefined || focusApplied) return false;
    const found = items.find((m) => m.id === focusId);
    if (!found || found.latitude == null || found.longitude == null) return false;
    map.setView([found.latitude, found.longitude], 14, { animate: false });
    viewReady = true;
    focusApplied = true;
    return true;
  }

  // Re-render markers whenever items change (e.g. include-hidden retry).
  // The `void items` read registers the dependency so $effect tracks
  // the prop reactively. Also re-attempt focus application: F7's hidden
  // retry sequence calls onMount before the second load resolves, so
  // focus may need to apply when the hidden row finally lands here.
  $effect(() => {
    void items;
    buildMarkers();
    tryApplyFocus();
    emitViewportVisible();
  });

  onDestroy(() => {
    if (map !== null) map.remove();
    map = null;
    cluster = null;
    markersById.clear();
    idByMarker.clear();
    itemsById.clear();
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

  /* Photo marker pin. Rendered into Leaflet's marker pane (outside this
     component's scope), so the rules need :global(...). 12×12 amber dot
     centered on its anchor; the soft ring reads as "photo here" without
     mimicking real photography. */
  :global(.map-photo-pin-wrap) {
    background: transparent;
    border: 0;
  }
  :global(.map-photo-pin) {
    display: block;
    width: 12px;
    height: 12px;
    border-radius: 50%;
    background: var(--amber);
    border: 1px solid var(--amber-deep);
    box-shadow:
      0 0 0 2px rgba(232, 164, 75, 0.18),
      inset 0 1px 0 rgba(255, 255, 255, 0.18);
  }

  /* Cluster pin. Single style across all sizes — no Leaflet
     small/medium/large color stages. The count uses the mono token
     with tabular-nums so 1/2/3-digit counts don't shift width. */
  :global(.map-cluster-pin-wrap) {
    background: transparent;
    border: 0;
  }
  :global(.map-cluster-pin) {
    display: flex;
    align-items: center;
    justify-content: center;
    background: color-mix(in srgb, var(--amber) 18%, var(--surface));
    border: 1px solid var(--amber);
    border-radius: 50%;
    box-shadow:
      0 0 0 3px rgba(232, 164, 75, 0.15),
      inset 0 1px 0 rgba(255, 255, 255, 0.12);
    color: var(--ink);
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: var(--text-sm);
    font-weight: 500;
  }

  /* Popup gallery — overrides Leaflet's default white rounded bubble
     with the app's dark surface tokens. Both the outer wrapper and the
     inner content node need styling: Leaflet draws its rounded chrome
     on .leaflet-popup-content-wrapper and the connector tail on
     .leaflet-popup-tip. */
  :global(.map-cluster-popup-shell .leaflet-popup-content-wrapper) {
    background: var(--surface);
    color: var(--ink);
    border: 1px solid var(--border);
    border-radius: 0;
    box-shadow:
      inset 0 1px 0 rgba(255, 255, 255, 0.05),
      0 1px 2px rgba(0, 0, 0, 0.4),
      0 1px 0 rgba(0, 0, 0, 0.45);
    padding: 0;
  }
  :global(.map-cluster-popup-shell .leaflet-popup-content) {
    margin: 0;
    width: auto !important;
  }
  :global(.map-cluster-popup-shell .leaflet-popup-tip) {
    background: var(--surface);
    border: 1px solid var(--border);
  }
  :global(.map-cluster-popup-shell a.leaflet-popup-close-button) {
    color: var(--ink-2);
    padding: 6px 8px 0 0;
  }
  :global(.map-cluster-popup-shell a.leaflet-popup-close-button:hover) {
    color: var(--ink);
  }

  :global(.map-cluster-popup) {
    display: flex;
    flex-direction: column;
    min-width: 240px;
    max-width: 260px;
    font-family: var(--font-ui);
  }
  :global(.map-cluster-popup__header) {
    padding: 8px 12px 6px;
    font-family: var(--font-mono);
    font-size: var(--text-xs);
    letter-spacing: var(--label-track);
    text-transform: uppercase;
    color: var(--ink-2);
    border-bottom: 1px solid var(--border);
  }
  :global(.map-cluster-popup__grid) {
    display: grid;
    grid-template-columns: repeat(4, 56px);
    grid-auto-rows: 56px;
    gap: 1px;
    background: var(--border);
    padding: 1px;
    max-height: 228px; /* ~4 rows × 56 + 4×1 gap */
    overflow-y: auto;
  }
  :global(.map-cluster-popup__cell) {
    width: 56px;
    height: 56px;
    background-color: var(--surface-2);
    background-size: cover;
    background-position: center;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  :global(.map-cluster-popup__cell:hover) {
    outline: 1px solid var(--amber);
    outline-offset: -1px;
  }
  :global(.map-cluster-popup__footer) {
    padding: 8px 12px;
    background: transparent;
    border: 0;
    border-top: 1px solid var(--border);
    color: var(--ink-3);
    font-family: var(--font-mono);
    font-size: var(--text-xs);
    text-align: left;
    cursor: pointer;
  }
  :global(.map-cluster-popup__footer:hover) {
    color: var(--ink-2);
  }
</style>

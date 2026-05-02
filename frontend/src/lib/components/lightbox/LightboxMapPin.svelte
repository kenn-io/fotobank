<!-- frontend/src/lib/components/lightbox/LightboxMapPin.svelte
     Static map preview that lives in the lightbox's metadata panel.
     Renders a non-interactive Leaflet tile + marker centered on the
     media's GPS coords. A separate "View on map" link below navigates
     to /map?focus=<id> so the user can inspect the location alongside
     other photos in the same area.

     Why the link is BELOW the map (not wrapping it): Leaflet's
     attribution control inserts its own <a href="..."> inside the map
     container. Wrapping the map in an outer <a> would create nested
     links — invalid HTML, and clicks on the attribution bubble up to
     our preventDefault, hijacking the OSM-credit click. Splitting the
     two means the attribution link works AND the focus link is
     explicit. Cmd/ctrl/shift/alt-click still falls through to the
     browser's default "open in new tab" behavior.
-->
<script lang="ts">
  import { onDestroy } from "svelte";
  import L from "leaflet";
  import "leaflet/dist/leaflet.css";
  import { tileUrl, attribution, defaultMaxZoom } from "../../map/tiles";
  import { router } from "../../router/router.svelte";

  // latitude/longitude must accept BOTH "absent property" (`?:`, matches
  // Media's optional fields when LightboxMetadata passes the row through)
  // AND "explicit undefined" (the test's pinProps helper sets undefined
  // explicitly). Combining `?:` with `| undefined` satisfies both under
  // exactOptionalPropertyTypes:true.
  let {
    media,
    navigate = (href: string): void => router.navigate(href),
  }: {
    media: {
      id: string;
      latitude?: number | null | undefined;
      longitude?: number | null | undefined;
      location_label?: string;
    };
    navigate?: (href: string) => void;
  } = $props();

  // Loose `!= null` covers null AND undefined; a truthiness check would
  // wrongly drop coordinates of exactly 0 (equator / prime meridian).
  const hasGPS = $derived(media.latitude != null && media.longitude != null);

  let container: HTMLDivElement | null = $state(null);
  let map: L.Map | null = null;
  let marker: L.Marker | null = null;

  // Re-build the map whenever the GPS-bearing media changes. Without
  // this, the lightbox arrow-walking from photo A to photo B (both
  // with GPS, metadata panel stays mounted) leaves the map stuck on
  // A's coords. The effect's dependency on media.id + lat + lon means
  // it re-runs precisely when the displayed photo changes.
  $effect(() => {
    if (!hasGPS || container === null) return;
    const lat = media.latitude as number;
    const lng = media.longitude as number;
    if (map === null) {
      map = L.map(container, {
        zoomControl: false,
        attributionControl: true,
        dragging: false,
        scrollWheelZoom: false,
        doubleClickZoom: false,
        touchZoom: false,
        boxZoom: false,
        keyboard: false,
      });
      L.tileLayer(tileUrl(), {
        attribution: attribution(),
        maxZoom: defaultMaxZoom,
      }).addTo(map);
    }
    // GPS → GPS navigation keeps this component mounted, so the effect
    // re-runs with new coords on the same map instance. Reuse the marker
    // (cheap setLatLng) instead of remove+addTo (which churns Leaflet's
    // internal layer set on every keypress).
    if (marker === null) {
      marker = L.marker([lat, lng]).addTo(map);
    } else {
      marker.setLatLng([lat, lng]);
    }
    map.setView([lat, lng], 14, { animate: false });
  });

  onDestroy(() => {
    if (map !== null) map.remove();
    map = null;
    marker = null;
  });

  const href = $derived(
    hasGPS
      ? `/map?z=14&c=${media.latitude},${media.longitude}&focus=${media.id}`
      : "#",
  );

  function onClick(e: MouseEvent): void {
    // Inline modifier-key gating instead of handleInternalLinkClick:
    // that helper calls router.navigate AND preventDefault, but we also
    // call the injected navigate prop, which would double-fire the
    // route change. Single navigation path keeps tests deterministic.
    if (e.button !== 0) return;
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    navigate(href);
  }
</script>

{#if hasGPS}
  <div class="map-pin" data-testid="lightbox-map-pin">
    <div class="map-pin-preview" bind:this={container}></div>
    {#if media.location_label}
      <div class="map-pin-label" title={media.location_label}>
        {media.location_label}
      </div>
    {/if}
    <a
      {href}
      onclick={onClick}
      class="map-pin-link"
      data-testid="lightbox-map-pin-link"
    >
      View on map →
    </a>
  </div>
{/if}

<style>
  .map-pin {
    display: block;
  }
  .map-pin-preview {
    width: 100%;
    aspect-ratio: 16 / 10;
    border-radius: 6px;
    overflow: hidden;
  }
  .map-pin-label {
    margin-top: 4px;
    font-size: 12px;
    color: var(--text-secondary, #6b7280);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .map-pin-link {
    display: inline-block;
    margin-top: 4px;
    font-size: 12px;
    color: var(--accent, #2563eb);
    text-decoration: none;
  }
  .map-pin-link:hover {
    text-decoration: underline;
  }
</style>

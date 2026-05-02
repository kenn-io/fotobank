<!-- frontend/src/lib/components/lightbox/LightboxMapPin.svelte
     Static map preview that lives in the lightbox's metadata panel.
     Renders a non-interactive Leaflet tile + marker centered on the
     media's GPS coords. Clicking the link navigates to /map?focus=<id>
     so the user can inspect the location alongside other photos in the
     same area. Cmd/ctrl/shift/alt-click is left alone so the browser
     can open in a new tab.
-->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import L from "leaflet";
  import "leaflet/dist/leaflet.css";
  import { tileUrl, attribution, defaultMaxZoom } from "../../map/tiles";
  import { router } from "../../router/router.svelte";

  // exactOptionalPropertyTypes:true distinguishes `?: T` (must be T when
  // present) from `T | undefined` (may be explicitly undefined). Callers
  // (LightboxMetadata, tests) pass explicit undefined for missing GPS,
  // so the union is required here.
  let {
    media,
    navigate = (href: string): void => router.navigate(href),
  }: {
    media: {
      id: string;
      latitude: number | null | undefined;
      longitude: number | null | undefined;
      location_label?: string;
    };
    navigate?: (href: string) => void;
  } = $props();

  // Loose `!= null` covers null AND undefined; a truthiness check would
  // wrongly drop coordinates of exactly 0 (equator / prime meridian).
  const hasGPS = $derived(media.latitude != null && media.longitude != null);

  let container: HTMLDivElement | null = $state(null);
  let map: L.Map | null = null;

  onMount(() => {
    if (!hasGPS || container === null) return;
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
    const lat = media.latitude as number;
    const lng = media.longitude as number;
    L.marker([lat, lng]).addTo(map);
    map.setView([lat, lng], 14, { animate: false });
  });

  onDestroy(() => {
    if (map !== null) map.remove();
    map = null;
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
  <a
    {href}
    onclick={onClick}
    data-testid="lightbox-map-pin"
    class="map-pin"
  >
    <div class="map-pin-preview" bind:this={container}></div>
    {#if media.location_label}
      <div class="map-pin-label" title={media.location_label}>
        {media.location_label}
      </div>
    {/if}
  </a>
{/if}

<style>
  .map-pin {
    display: block;
    text-decoration: none;
    color: inherit;
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
</style>

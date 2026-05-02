<!-- frontend/src/routes/Map.svelte
     Route component for /map. F2 ships the page shell only — F3 will
     fill in the Leaflet pane and right-side photo grid. The component
     receives geoStore as a prop so tests can construct the store with
     a fake typed-client without stubbing global fetch.
-->
<script lang="ts">
  import { onMount } from "svelte";
  import type { GeoStore } from "../lib/map/geoStore.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";

  // F2 only consumes geoStore. The remaining props are typed up-front so
  // F3-F9 can wire them in without changing the call site in App.svelte.
  // The route-derived params (z, c, focus, tab) use `T | undefined` (not
  // `?:`) because `exactOptionalPropertyTypes:true` rejects assigning
  // `undefined` to a `?` optional, and the router can supply undefined.
  let {
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
    <!-- F3 fills in <MapPane /> and <MapGridPane />. -->
    <div data-testid="map-loaded">{geoStore.items.length} photos with GPS</div>
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
</style>

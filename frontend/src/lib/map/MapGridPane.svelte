<!-- frontend/src/lib/map/MapGridPane.svelte
     Right-side grid for the /map page. Renders the cluster-filter set
     when present, otherwise the viewport-visible set. VirtualGrid is
     driven through a synthetic single-month feed (timelineChrome=false)
     so the date-axis chrome is suppressed — same pattern as
     AlbumDetail.svelte.
-->
<script lang="ts">
  import VirtualGrid from "../grid/VirtualGrid.svelte";
  import type { MediaStore, Month, Media } from "../media/mediaStore.svelte";
  import type { GeoStore } from "./geoStore.svelte";

  let {
    visibleIds,
    clusterIds,
    onPhotoClick,
    onClearClusterFilter,
    mediaStore,
    geoStore,
  }: {
    visibleIds: string[];
    clusterIds: string[] | null;
    onPhotoClick: (id: string) => void;
    onClearClusterFilter: () => void;
    mediaStore: MediaStore;
    geoStore: GeoStore;
  } = $props();

  const activeIds = $derived(clusterIds !== null ? clusterIds : visibleIds);
  const showClearChip = $derived(clusterIds !== null);

  // Synthetic single-month feed for VirtualGrid timelineChrome=false.
  // The lookup chain prefers MediaStore (visible rows pre-merged via
  // Map.svelte's loadAndMerge) and falls back to GeoStore for hidden
  // rows — MediaStore.mergeRaw drops hidden_at!=null by design, so
  // hidden geo rows would otherwise vanish from the right-grid even
  // when their markers exist on the map.
  // Touching mediaStore.months and geoStore.items registers reactive
  // deps on both sources; the derivation re-runs on either change.
  const months: Month[] = $derived.by((): Month[] => {
    void mediaStore.months;
    void geoStore.items;
    const items: Media[] = activeIds
      .map((id) => mediaStore.get(id) ?? geoStore.findById(id))
      .filter((m): m is Media => m !== undefined);
    return [{ key: "map:active", items }];
  });
</script>

<div class="map-grid-pane" data-testid="map-grid-pane">
  <header>
    <span>{activeIds.length} photos</span>
    {#if showClearChip}
      <button class="clear-chip" onclick={onClearClusterFilter}>× Clear filter</button>
    {/if}
  </header>
  {#if activeIds.length === 0}
    <div class="empty">No photos in view. Pan or zoom to find your photos.</div>
  {:else}
    <VirtualGrid {months} timelineChrome={false} onOpenMedia={onPhotoClick} />
  {/if}
</div>

<style>
  .map-grid-pane {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    border-left: 1px solid var(--border, #e5e7eb);
  }
  header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border, #e5e7eb);
  }
  .clear-chip {
    margin-left: auto;
    background: transparent;
    border: 1px solid var(--border, #e5e7eb);
    border-radius: 12px;
    padding: 2px 10px;
    cursor: pointer;
    font-size: 12px;
  }
  .empty {
    padding: 24px;
    color: var(--ink-2);
  }
</style>

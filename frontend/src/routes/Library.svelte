<!-- frontend/src/routes/Library.svelte -->
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import { MediaStore } from "../lib/media/mediaStore.svelte";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import { api } from "../lib/api/client";

  const store = new MediaStore(api);
  store.loadInitial();

  const density = new DensityStore(api, "library");
  density.load();
</script>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>

<VirtualGrid
  months={store.months}
  onLoadMore={() => store.loadMore()}
  targetRowHeight={density.targetRowHeight}
/>

{#if store.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if store.months.length === 0 && !store.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}

<!-- frontend/src/routes/Library.svelte -->
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import { MediaStore } from "../lib/media/mediaStore.svelte";
  import { api } from "../lib/api/client";

  const store = new MediaStore(api);
  store.loadInitial();
</script>

<VirtualGrid
  months={store.months}
  onLoadMore={() => store.loadMore()}
  targetRowHeight={200}
/>

{#if store.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if store.months.length === 0 && !store.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}

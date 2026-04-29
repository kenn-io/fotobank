<!-- frontend/src/routes/Library.svelte -->
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import GroupSelectButton from "../lib/components/GroupSelectButton.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import { api } from "../lib/api/client";

  let { mediaStore }: { mediaStore: MediaStore } = $props();

  const density = new DensityStore(api, "library");
  density.load();
</script>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>

<VirtualGrid
  months={mediaStore.months}
  onLoadMore={() => mediaStore.loadMore()}
  targetRowHeight={density.targetRowHeight}
>
  {#snippet headerAction(month)}
    <GroupSelectButton
      ids={month.items.map((it) => it.id)}
      label={month.key}
    />
  {/snippet}
</VirtualGrid>

{#if mediaStore.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if mediaStore.months.length === 0 && !mediaStore.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}

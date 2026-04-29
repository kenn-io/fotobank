<!-- frontend/src/routes/HiddenLibrary.svelte
     Route component for /hidden. Wraps HiddenGate and shows a grid of
     hidden media when unlocked. HiddenMediaStore is route-scoped:
     created here and torn down when this component unmounts.
-->
<script lang="ts">
  import HiddenGate from "../lib/components/HiddenGate.svelte";
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
  import { HiddenMediaStore } from "../lib/hidden/hiddenMediaStore.svelte";
  import { api } from "../lib/api/client";

  let {
    hiddenStore,
    // Optional override for testing. When omitted, the route constructs
    // its own route-scoped store backed by the live api client.
    hiddenMediaStore = new HiddenMediaStore(api),
  }: {
    hiddenStore: HiddenStore;
    hiddenMediaStore?: HiddenMediaStore;
  } = $props();

  // Load initial items when the store is configured and unlocked.
  $effect(() => {
    if (hiddenStore.configured && hiddenStore.unlocked) {
      void hiddenMediaStore.loadInitial();
    }
  });
</script>

<HiddenGate {hiddenStore}>
  {#snippet children()}
    <VirtualGrid
      months={hiddenMediaStore.months}
      onLoadMore={() => hiddenMediaStore.loadMore()}
    />

    {#if hiddenMediaStore.loading}
      <div style="padding:12px; color: var(--text-muted)">Loading…</div>
    {/if}
    {#if hiddenMediaStore.months.length === 0 && !hiddenMediaStore.loading}
      <div style="padding:24px; color: var(--text-secondary)">No hidden photos.</div>
    {/if}
  {/snippet}
</HiddenGate>

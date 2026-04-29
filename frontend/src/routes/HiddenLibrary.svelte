<!-- frontend/src/routes/HiddenLibrary.svelte
     Route component for /hidden. Wraps HiddenGate and shows a grid of
     hidden media when unlocked. HiddenMediaStore is route-scoped:
     created here and torn down when this component unmounts.
-->
<script lang="ts">
  import HiddenGate from "../lib/components/HiddenGate.svelte";
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import ActionBar from "../lib/components/ActionBar.svelte";
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import { selection } from "../lib/selection/selectionStore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
  import { HiddenMediaStore } from "../lib/hidden/hiddenMediaStore.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import { api } from "../lib/api/client";

  let {
    hiddenStore,
    albumsStore,
    toastStore,
    // Optional override for testing. When omitted, the route constructs
    // its own route-scoped store backed by the live api client.
    hiddenMediaStore = new HiddenMediaStore(api),
  }: {
    hiddenStore: HiddenStore;
    albumsStore: AlbumsStore;
    toastStore: ToastStore;
    hiddenMediaStore?: HiddenMediaStore;
  } = $props();

  // Load initial items when the store is configured and unlocked.
  $effect(() => {
    if (hiddenStore.configured && hiddenStore.unlocked) {
      void hiddenMediaStore.loadInitial();
    }
  });

  let addOpen = $state(false);
  let pendingIds = $state<string[]>([]);
  function openAdd(ids: string[]) { pendingIds = ids; addOpen = true; }

  async function onAdd(albumId: string): Promise<{ added: number; already_present: number }> {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingIds } as never,
    });
    if (res.error) throw res.error;
    selection.clear();
    return res.data as { added: number; already_present: number };
  }

  async function onUnhide(ids: string[]): Promise<void> {
    if (!window.confirm(`Unhide ${ids.length} photo${ids.length === 1 ? "" : "s"}?`)) return;
    let result;
    try {
      result = await hiddenStore.unhide(ids);
    } catch {
      toastStore.push({ message: "Unhide failed. Try again.", kind: "error" });
      return;
    }
    const succeeded = result.succeeded ?? [];
    if (succeeded.length > 0) {
      hiddenMediaStore.removeMany(succeeded);
      selection.removeAll(succeeded);
      // Mark albums stale so counts refresh on next visit.
      albumsStore.loadInitial();
    }
    const failed = result.failed ?? [];
    if (failed.length > 0) {
      toastStore.push({
        message: `${failed.length} photo${failed.length === 1 ? "" : "s"} could not be unhidden.`,
        details: failed.map((f) => `${f.id}: ${f.code}`),
        kind: "error",
      });
    }
  }
</script>

<HiddenGate {hiddenStore}>
  {#snippet children()}
    <ActionBar {selection}>
      {#snippet actions()}
        <MediaActions
          mediaIds={Array.from(selection.ids)}
          context="hidden"
          onAdd={openAdd}
          onShare={() => {}}
          {onUnhide}
        />
      {/snippet}
    </ActionBar>

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

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}

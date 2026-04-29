<!-- frontend/src/routes/Library.svelte -->
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import GroupSelectButton from "../lib/components/GroupSelectButton.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import ActionBar from "../lib/components/ActionBar.svelte";
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import ShareModal from "../lib/components/ShareModal.svelte";
  import { selection } from "../lib/selection/selectionStore.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import type { CreateShareBody } from "../lib/share/shareTypes";
  import { api } from "../lib/api/client";

  let { mediaStore, albumsStore }: { mediaStore: MediaStore; albumsStore: AlbumsStore } = $props();

  const density = new DensityStore(api, "library");
  density.load();

  let addOpen = $state(false);
  let shareOpen = $state(false);
  let pendingIds = $state<string[]>([]);

  function openAdd(ids: string[]) { pendingIds = ids; addOpen = true; }
  function openShare(ids: string[]) { pendingIds = ids; shareOpen = true; }

  async function onAdd(albumId: string): Promise<{ added: number; already_present: number }> {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingIds } as never,
    });
    if (res.error) throw res.error;
    selection.clear();
    return res.data as { added: number; already_present: number };
  }

  async function onCreateShare(body: CreateShareBody): Promise<void> {
    const res = await api.POST("/api/v1/shares", { body: body as never });
    if (res.error) throw res.error;
    // ShareModal calls onClose() itself on success — match the
    // AddToAlbumModal contract; no need to flip shareOpen here.
    selection.clear();
  }
</script>

<ActionBar {selection}>
  {#snippet actions()}
    <MediaActions
      mediaIds={Array.from(selection.ids)}
      onAdd={openAdd}
      onShare={openShare}
    />
  {/snippet}
</ActionBar>

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

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}

{#if shareOpen}
  <ShareModal
    target={{ type: "media_set", mediaIds: pendingIds }}
    onCreate={onCreateShare}
    onClose={() => (shareOpen = false)}
  />
{/if}

<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import type { MediaStore, Month, Media } from "../lib/media/mediaStore.svelte";
  import { AlbumDetailStore, type AlbumSort } from "../lib/albums/albumDetailStore.svelte";
  import { api } from "../lib/api/client";
  import RenameAlbumModal from "../lib/components/RenameAlbumModal.svelte";
  import ConfirmModal from "../lib/components/ConfirmModal.svelte";
  import ActionBar from "../lib/components/ActionBar.svelte";
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import ShareModal from "../lib/components/ShareModal.svelte";
  import { selection } from "../lib/selection/selectionStore.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import type { CreateShareBody } from "../lib/share/shareTypes";
  import { router, handleInternalLinkClick } from "../lib/router/router.svelte";

  let { id, mediaStore, albumsStore }: {
    id: string;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
  } = $props();

  // AlbumDetailStore needs a stable MediaStore reference for its
  // lifetime — recreating it on every reactive read would lose
  // pagination state (nextOffset, membership, itemIds). App.svelte
  // constructs mediaStore once at boot and passes it by stable
  // reference, so capturing the script-top prop value is correct here.
  // svelte-ignore state_referenced_locally
  const detail = new AlbumDetailStore(api, mediaStore);

  $effect(() => {
    detail.load(id);
  });

  // Synthetic single-month feed for VirtualGrid timelineChrome=false.
  // mediaStore.get(...) reads from a non-reactive Map, so we touch
  // the reactive `months` snapshot first to register a dependency —
  // every MediaStore.merge() replaces `months`, forcing this derivation
  // to re-run when new media rows arrive (e.g. SSE updates, thumb
  // version bumps). Same pattern as MediaDetail.svelte:14.
  const months: Month[] = $derived.by((): Month[] => {
    void mediaStore.months;
    if (!detail.album) return [];
    const items: Media[] = detail.itemIds
      .map((mid) => mediaStore.get(mid))
      .filter((m): m is Media => m !== undefined);
    return [{ key: `album:${id}`, items }];
  });

  // Route-scoped selection: only the global-selection ids that are
  // actually members of THIS album. Reading detail.itemIds inside the
  // derivation registers a reactive dep — membership is a non-reactive
  // Set, but every loadMore/removeMany pairs a membership mutation with
  // an itemIds reassignment, so itemIds is the right reactive proxy.
  const selectedInAlbum = $derived.by((): string[] => {
    void detail.itemIds;
    return Array.from(selection.ids).filter((sid) => detail.hasInAlbum(sid));
  });

  function loadMore() { detail.loadMore(); }

  function changeSort(e: Event) {
    const next = (e.target as HTMLSelectElement).value as AlbumSort;
    detail.setSort(next);
  }

  let renaming = $state(false);
  let confirmingDelete = $state(false);
  let deleteConflictAlbumId = $state<string | null>(null);

  let addOpen = $state(false);
  let shareOpen = $state(false);
  let shareAlbumOpen = $state(false);
  let pendingIds = $state<string[]>([]);

  // The router reuses this component instance across /albums/:id
  // navigations, so the three modal flags and pendingIds would carry
  // over from the previous album. Reset them whenever id changes so
  // navigating away from an album with an open modal doesn't leave that
  // modal showing the prior album's selection. `void id` registers the
  // reactive dep without using the value (same pattern as the
  // mediaStore.months access in the months derivation above).
  $effect(() => {
    void id;
    addOpen = false;
    shareOpen = false;
    shareAlbumOpen = false;
    pendingIds = [];
  });

  function openAdd(ids: string[]) { pendingIds = ids; addOpen = true; }
  function openShare(ids: string[]) { pendingIds = ids; shareOpen = true; }

  async function onRemove(ids: string[]): Promise<void> {
    const result = await detail.removeMany(ids);
    if (result.succeeded.length > 0) {
      selection.removeAll(result.succeeded);
    }
    if (result.failed.length > 0) {
      // Toast surface lands later; for now log the partial-failure ids
      // so a developer can investigate without a silent drop.
      console.warn("partial remove failure:", result.failed);
    }
  }

  async function onAdd(albumId: string): Promise<{ added: number; already_present: number }> {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingIds } as never,
    });
    if (res.error) throw res.error;
    selection.clear();
    if (albumId === id) {
      // Adding to the current album → refetch this view so the count
      // and item list reflect the new membership.
      await detail.load(id);
    }
    return res.data as { added: number; already_present: number };
  }

  async function onCreateShare(body: CreateShareBody): Promise<void> {
    const res = await api.POST("/api/v1/shares", { body: body as never });
    if (res.error) throw res.error;
    // ShareModal calls onClose() itself on success — just clear the
    // selection. Whichever modal owns the open flag (shareOpen or
    // shareAlbumOpen) closes via its own bound onClose handler.
    selection.clear();
  }

  async function onRename(name: string) {
    await detail.rename(name);
    renaming = false;
  }

  async function onConfirmDelete() {
    // Capture id before the await — if the user navigates between albums
    // while delete is in flight, the route's `id` would already point at
    // a different album by the time the 409 toast fires.
    const albumId = id;
    try {
      await detail.delete();
      router.navigate("/albums");
    } catch (e: unknown) {
      confirmingDelete = false;
      const status = (e as { status?: number })?.status;
      if (status === 409) {
        deleteConflictAlbumId = albumId;
      } else {
        // Generic toast surface lands later; for now, log.
        console.error("delete failed:", e);
      }
    }
  }
</script>

{#if detail.metaLoading && !detail.album}
  <div class="loading">Loading album…</div>
{:else if detail.metaError}
  <div class="empty">
    Album not found.<br />
    It may have been deleted, or you might not have access.
  </div>
{:else if detail.album}
  <header class="album-header">
    <div class="title-row">
      <h1>{detail.album.name}</h1>
      <span class="count">{detail.album.item_count} {detail.album.item_count === 1 ? "photo" : "photos"}</span>
    </div>
    <div class="action-row">
      <div class="actions">
        <button type="button" onclick={() => (renaming = true)}>Rename</button>
        <button type="button" onclick={() => (shareAlbumOpen = true)} disabled={!detail.album || detail.album.item_count === 0}>
          Share album
        </button>
        <button type="button" class="danger" onclick={() => (confirmingDelete = true)}>Delete</button>
      </div>
      <label class="sort">
        Sort:
        <select value={detail.sort} onchange={changeSort} disabled={detail.loading}>
          <option value="taken">Date taken</option>
          <option value="added">Recently added</option>
        </select>
      </label>
    </div>
  </header>
{/if}

<ActionBar {selection} selectedCount={selectedInAlbum.length}>
  {#snippet actions()}
    {#if selectedInAlbum.length > 0}
      <MediaActions
        mediaIds={selectedInAlbum}
        context="album"
        albumId={id}
        onAdd={openAdd}
        onShare={openShare}
        {onRemove}
      />
    {/if}
  {/snippet}
</ActionBar>

{#if renaming && detail.album}
  <RenameAlbumModal
    initialName={detail.album.name}
    onSave={onRename}
    onCancel={() => (renaming = false)}
  />
{/if}

{#if confirmingDelete && detail.album}
  <ConfirmModal
    title={`Delete '${detail.album.name}'?`}
    body="Photos remain in your library."
    confirmLabel="Delete"
    danger
    onConfirm={onConfirmDelete}
    onCancel={() => (confirmingDelete = false)}
  />
{/if}

{#if deleteConflictAlbumId}
  <div class="conflict-toast" role="alert">
    This album has active shares. Revoke them in Shares first.
    <a
      href={`/shares?album_id=${deleteConflictAlbumId}`}
      onclick={(e) => handleInternalLinkClick(e, `/shares?album_id=${deleteConflictAlbumId}`)}
    >
      View shares →
    </a>
    <button type="button" onclick={() => (deleteConflictAlbumId = null)}>×</button>
  </div>
{/if}

{#if detail.itemIds.length > 0}
  <VirtualGrid
    {months}
    onLoadMore={loadMore}
    targetRowHeight={200}
    timelineChrome={false}
  />
{:else if !detail.loading && detail.album}
  <div class="empty">
    Empty album.<br />
    Select photos in Library, then use <strong>Add to album</strong> in the action bar.
  </div>
{/if}

{#if detail.loading}<div class="loading">Loading…</div>{/if}

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

{#if shareAlbumOpen && detail.album}
  <ShareModal
    target={{ type: "album_live", albumId: id, albumName: detail.album.name }}
    onCreate={onCreateShare}
    onClose={() => (shareAlbumOpen = false)}
  />
{/if}

<style>
  .album-header {
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .title-row { display: flex; align-items: baseline; gap: 12px; }
  .title-row h1 { margin: 0; font-size: 20px; }
  .count { color: var(--text-muted); font-size: 14px; }
  .action-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-top: 8px;
  }
  .actions { display: flex; gap: 8px; }
  .sort { font-size: 13px; color: var(--text-muted); }
  .empty { padding: 64px 16px; text-align: center; color: var(--text-secondary); }
  .loading { padding: 12px; color: var(--text-muted); }
  .danger { background: var(--danger); color: white; border-color: var(--danger); }
  .conflict-toast {
    position: fixed; bottom: 16px; right: 16px;
    background: var(--bg-elevated); border: 1px solid var(--danger);
    padding: 12px 16px; border-radius: 6px; z-index: 200;
    display: flex; align-items: center; gap: 12px;
  }
  .conflict-toast a { color: var(--accent); text-decoration: underline; }
</style>

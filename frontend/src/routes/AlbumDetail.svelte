<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import type { MediaStore, Month, Media } from "../lib/media/mediaStore.svelte";
  import { AlbumDetailStore, type AlbumSort } from "../lib/albums/albumDetailStore.svelte";
  import { api } from "../lib/api/client";
  import RenameAlbumModal from "../lib/components/RenameAlbumModal.svelte";
  import ConfirmModal from "../lib/components/ConfirmModal.svelte";
  import { router, handleInternalLinkClick } from "../lib/router/router.svelte";

  let { id, mediaStore }: { id: string; mediaStore: MediaStore } = $props();

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

  function loadMore() { detail.loadMore(); }

  function changeSort(e: Event) {
    const next = (e.target as HTMLSelectElement).value as AlbumSort;
    detail.setSort(next);
  }

  let renaming = $state(false);
  let confirmingDelete = $state(false);
  let deleteConflictAlbumId = $state<string | null>(null);

  async function onRename(name: string) {
    await detail.rename(name);
    renaming = false;
  }

  async function onConfirmDelete() {
    try {
      await detail.delete();
      router.navigate("/albums");
    } catch (e: unknown) {
      confirmingDelete = false;
      const status = (e as { status?: number })?.status;
      if (status === 409) {
        deleteConflictAlbumId = id;
      } else {
        // Generic toast surface lands later; for now, log.
        console.error("delete failed:", e);
      }
    }
  }
</script>

{#if detail.album}
  <header class="album-header">
    <div class="title-row">
      <h1>{detail.album.name}</h1>
      <span class="count">{detail.album.item_count} {detail.album.item_count === 1 ? "photo" : "photos"}</span>
    </div>
    <div class="action-row">
      <div class="actions">
        <button type="button" onclick={() => (renaming = true)}>Rename</button>
        <button type="button" class="danger" onclick={() => (confirmingDelete = true)}>Delete</button>
      </div>
      <label class="sort">
        Sort:
        <select value={detail.sort} onchange={changeSort}>
          <option value="taken">Date taken</option>
          <option value="added">Recently added</option>
        </select>
      </label>
    </div>
  </header>
{/if}

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

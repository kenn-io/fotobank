<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import type { MediaStore, Month, Media } from "../lib/media/mediaStore.svelte";
  import { AlbumDetailStore, type AlbumSort } from "../lib/albums/albumDetailStore.svelte";
  import { api } from "../lib/api/client";

  let { id, mediaStore }: { id: string; mediaStore: MediaStore } = $props();

  // mediaStore is constructed once at App boot and passed by stable
  // reference; capturing it in the AlbumDetailStore constructor is
  // intentional. The Svelte compiler can't know the prop is stable.
  // svelte-ignore state_referenced_locally
  const detail = new AlbumDetailStore(api, mediaStore);

  $effect(() => {
    detail.load(id);
  });

  // Synthetic single-month feed for VirtualGrid timelineChrome=false.
  // Use $derived.by(...) — the codebase pattern for multi-statement
  // deriveds (see MediaDetail.svelte:14). $derived(() => ...) would
  // yield a function-valued derived, not what we want.
  const months: Month[] = $derived.by((): Month[] => {
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
</script>

{#if detail.album}
  <header class="album-header">
    <div class="title-row">
      <h1>{detail.album.name}</h1>
      <span class="count">{detail.album.item_count} {detail.album.item_count === 1 ? "photo" : "photos"}</span>
    </div>
    <div class="action-row">
      <div class="actions"><!-- Rename / Delete / Share buttons land in Tasks 14, 19 --></div>
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
</style>

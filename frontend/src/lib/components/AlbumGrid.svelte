<script lang="ts">
  import { handleInternalLinkClick } from "../router/router.svelte";
  import type { AlbumListItem } from "../albums/albumsStore.svelte";

  let { albums }: { albums: AlbumListItem[] } = $props();

  function thumbUrl(a: AlbumListItem): string | null {
    if (!a.cover) return null;
    return `/api/v1/media/${a.cover.media_id}/thumb?size=grid&v=${a.cover.thumb_version}`;
  }
</script>

<div class="album-grid">
  {#each albums as a (a.id)}
    <a
      class="tile"
      href={`/albums/${a.id}`}
      onclick={(e) => handleInternalLinkClick(e, `/albums/${a.id}`)}
    >
      {#if thumbUrl(a)}
        <img class="cover" src={thumbUrl(a)} alt={a.name} loading="lazy" />
      {:else}
        <div class="cover placeholder" aria-hidden="true">
          <span class="icon">▢</span>
        </div>
      {/if}
      <div class="meta">
        <div class="name" title={a.name}>{a.name}</div>
        <div class="count">{a.item_count} {a.item_count === 1 ? "photo" : "photos"}{(a.hidden_count ?? 0) > 0 ? ` · ${a.hidden_count} hidden` : ""}</div>
      </div>
    </a>
  {/each}
</div>

<style>
  .album-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 12px;
    padding: 12px;
  }
  .tile {
    display: flex;
    flex-direction: column;
    text-decoration: none;
    color: var(--text-primary);
  }
  .cover {
    aspect-ratio: 1 / 1;
    object-fit: cover;
    width: 100%;
    border-radius: 6px;
    background: var(--bg-elevated);
  }
  .placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border);
    color: var(--text-muted);
  }
  .placeholder .icon { font-size: 32px; }
  .meta { padding: 6px 4px; }
  .name {
    font-size: 14px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .count { font-size: 12px; color: var(--text-muted); }
</style>

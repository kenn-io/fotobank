<!-- frontend/src/lib/grid/MediaCell.svelte -->
<script lang="ts">
  import type { MediaLite } from "./monthChunkLayout";

  let { media, selected, onCellClick }: {
    media: MediaLite;
    selected: boolean;
    onCellClick: (e: MouseEvent) => void;
  } = $props();

  // Tracks whether the current thumb URL has 404'd in this cell. F2.0
  // operators trigger this by running `fotobank thumbs regenerate`,
  // which bumps thumb_version on disk; cached <img> elements continue
  // to point at v=N until a refetch, and the v=N thumb returns 404
  // until the worker drains. Showing a neutral placeholder is much
  // better than the browser's broken-image glyph.
  let imgError = $state(false);

  // Reset the error flag whenever media.thumbUrl changes, so once a
  // refetch lands the v=N+1 URL we re-attempt the load. Without this
  // the cell is stuck on the placeholder for the lifetime of the
  // component even after the worker drains.
  $effect(() => {
    media.thumbUrl;
    imgError = false;
  });
</script>

<a
  href={`/media/${media.id}`}
  aria-label={`Photo ${media.id}`}
  class:selected
  onclick={onCellClick}
>
  {#if !imgError && media.thumbUrl}
    <img
      src={media.thumbUrl}
      alt=""
      loading="lazy"
      decoding="async"
      style="width:100%;height:100%;object-fit:cover"
      onerror={() => (imgError = true)}
    />
  {:else}
    <div class="placeholder" aria-hidden="true"></div>
  {/if}
</a>

<style>
  a {
    display: block;
    width: 100%;
    height: 100%;
  }
  a.selected {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
    border-radius: 2px;
  }
  .placeholder {
    width: 100%;
    height: 100%;
    background: var(--bg-elevated);
    border-radius: 2px;
  }
</style>

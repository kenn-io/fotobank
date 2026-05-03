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

  // Reset imgError ONLY when the URL string actually changes — not on
  // every parent rerender. VirtualGrid's toLite() allocates a fresh
  // MediaLite object per render even when underlying fields are
  // unchanged; without this gate every parent rerender would clear
  // imgError and re-attempt the same broken URL, defeating the
  // placeholder fallback. Comparing to the previous URL string makes
  // the reset key the actual URL identity, not the prop reference.
  let lastThumbUrl: string | undefined = undefined;
  $effect(() => {
    if (media.thumbUrl !== lastThumbUrl) {
      lastThumbUrl = media.thumbUrl;
      imgError = false;
    }
  });
</script>

<a
  href={`/media/${media.id}`}
  data-media-id={media.id}
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
    outline: 2px solid var(--amber);
    outline-offset: -2px;
    border-radius: 2px;
  }
  .placeholder {
    width: 100%;
    height: 100%;
    background: var(--surface-2);
    border-radius: 2px;
  }
</style>

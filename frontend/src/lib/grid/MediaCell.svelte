<!-- frontend/src/lib/grid/MediaCell.svelte -->
<script lang="ts">
  import { Checkbox } from "@kenn-io/kit-ui";
  import type { MediaLite } from "./monthChunkLayout";

  let { media, selected, onCellClick, onSelect }: {
    media: MediaLite;
    selected: boolean;
    onCellClick: (e: MouseEvent) => void;
    onSelect: (selected: boolean) => void;
  } = $props();

  // Tracks whether the current thumb URL has 404'd in this cell.
  // Reaching the error path is rare: thumbStatus is the primary gate,
  // so we only attempt to load when the backend says the thumb is
  // ready. A 404 in that state means the on-disk file disappeared
  // between the API list and the <img> request (e.g. regenerate
  // bumped thumb_version mid-page-load).
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

  // Cell renders one of three visual states. "ready" attempts the
  // <img>; "loading" (pending/working OR a transient img error)
  // shows a shimmer so the user can see the worker is making
  // progress; "blank" (failed/no_preview) shows a static placeholder
  // so terminal-failure cells don't pulse forever. Default when
  // thumbStatus is missing is "loading" — same rationale as the
  // store default: a missing field is "still pending", never failed.
  const visual = $derived(
    media.thumbStatus === "ready" && !imgError
      ? "ready"
      : media.thumbStatus === "failed" || media.thumbStatus === "no_preview"
        ? "blank"
        : "loading",
  );
</script>

<div class="media-cell">
  <a
    href={`/media/${media.id}`}
    data-media-id={media.id}
    data-thumb-status={media.thumbStatus ?? "pending"}
    aria-label={`Photo ${media.id}`}
    class:selected
    onclick={onCellClick}
  >
    {#if visual === "ready" && media.thumbUrl}
      <img
        src={media.thumbUrl}
        alt=""
        loading="lazy"
        decoding="async"
        style="width:100%;height:100%;object-fit:cover"
        onerror={() => (imgError = true)}
      />
    {:else if visual === "loading"}
      <div class="shimmer" aria-label="Thumbnail still processing"></div>
    {:else}
      <div class="placeholder" aria-hidden="true"></div>
    {/if}
  </a>
  <Checkbox
    class="photo-select"
    ariaLabel={`Select ${media.id}`}
    checked={selected}
    onchange={onSelect}
  />
</div>

<style>
  .media-cell { position: relative; width: 100%; height: 100%; }
  .media-cell :global(.photo-select) {
    position: absolute;
    top: 0;
    left: 0;
    /* Leave the photo link reachable even in short panorama rows. */
    width: min(44px, 40%);
    height: min(44px, 100%);
    justify-content: center;
    background: color-mix(in srgb, var(--bg-surface) 85%, transparent);
    border-radius: 0 0 4px 0;
  }
  .media-cell :global(.kit-checkbox__box) { max-width: 100%; max-height: 100%; }
  a {
    display: block;
    width: 100%;
    height: 100%;
  }
  a.selected {
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
    border-radius: 2px;
  }
  .placeholder {
    width: 100%;
    height: 100%;
    background: var(--bg-inset);
    border-radius: 2px;
  }
  /* Shimmer reads as "in flight" without spinning. A diagonal sheen
     drifts across the cell on a 1.6s loop. The base color matches
     .placeholder so cells whose status flips between visual states
     don't introduce a luminance pop. */
  .shimmer {
    width: 100%;
    height: 100%;
    border-radius: 2px;
    background: linear-gradient(
      110deg,
      var(--bg-inset) 30%,
      color-mix(in srgb, var(--bg-inset) 70%, var(--text-muted)) 50%,
      var(--bg-inset) 70%
    );
    background-size: 220% 100%;
    animation: shimmer 1.6s linear infinite;
  }
  @keyframes shimmer {
    0%   { background-position: 100% 0; }
    100% { background-position: -100% 0; }
  }
  /* Reduced motion: drop the animation, keep the shimmer color so
     loading cells still read as distinct from terminal-failure ones. */
  @media (prefers-reduced-motion: reduce) {
    .shimmer { animation: none; }
  }
</style>

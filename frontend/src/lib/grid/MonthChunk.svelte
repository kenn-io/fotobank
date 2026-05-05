<script lang="ts">
  import type { Snippet } from "svelte";
  import type { LayoutOptions } from "./justifiedLayout";
  import { computeMonthLayout, type MediaLite } from "./monthChunkLayout";

  // inWindow gates cell rendering: when false the chunk renders only
  // its `min-height` placeholder (preserving scroll math); when true
  // the cells grid renders normally. VirtualGrid sets it based on an
  // IntersectionObserver with a generous rootMargin so chunks far
  // from the scroll position drop their DOM/listeners. Default true
  // so callers that don't pass the prop (tests, AlbumDetail flat
  // mode) keep the previous behavior.
  let { items, options, label, renderCell, headerAction, inWindow = true }: {
    items: MediaLite[];
    options: LayoutOptions;
    label?: string;
    renderCell?: Snippet<[MediaLite, { x: number; y: number; w: number; h: number }]>;
    headerAction?: Snippet;
    inWindow?: boolean;
  } = $props();

  let computed = $derived(computeMonthLayout(items, options, !!label));
</script>

<!-- content-visibility:auto on cells was removed in PS-3e (#30). The
     four-variant A/B in tests/e2e/scale/library.spec.ts showed c-v
     causes longtasks even under realistic dwell-style scroll
     (5 longtasks / 116ms max frame in a 15s window, vs 0 / 33ms
     without). The benefit c-v could provide — deferring img.decode
     for offscreen thumbs — wasn't measurable because the test seeds
     no real thumb files. PS-3e2 (kata follow-up) re-tests with real
     images before considering re-adding c-v. -->
<section class="month" style="min-height: {computed.intrinsicHeight}px;">
  {#if inWindow}
    {#if label}
      <header class="day-header">
        <span class="label">{label}</span>
        {#if headerAction}
          <span class="header-action">{@render headerAction()}</span>
        {/if}
      </header>
    {/if}
    <div class="cells" style="position: relative; height: {computed.layout.totalHeight}px;">
      {#each computed.layout.rows as row (row.y)}
        {#each row.items as cell (cell.index)}
          {@const m = items[cell.index]}
          {#if m}
            <div
              class="cell"
              style="position: absolute; left: {cell.x}px; top: {row.y}px;
                     width: {cell.width}px; height: {row.height}px;"
            >
              {#if renderCell}
                {@render renderCell(m, { x: cell.x, y: row.y, w: cell.width, h: row.height })}
              {:else}
                <div class="placeholder"></div>
              {/if}
            </div>
          {/if}
        {/each}
      {/each}
    </div>
  {/if}
</section>

<style>
  .month { display: block; }
  .day-header {
    font-size: 12px;
    color: var(--ink-2);
    padding: 16px 4px 8px;
    font-weight: 500;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .header-action {
    margin-left: auto;
  }
  .placeholder {
    width: 100%;
    height: 100%;
    background: var(--surface-2);
    border-radius: 2px;
  }
</style>

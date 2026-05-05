<script lang="ts">
  import type { Snippet } from "svelte";
  import type { LayoutOptions } from "./justifiedLayout";
  import { computeMonthLayout, type MediaLite } from "./monthChunkLayout";

  let { items, options, label, renderCell, headerAction }: {
    items: MediaLite[];
    options: LayoutOptions;
    label?: string;
    renderCell?: Snippet<[MediaLite, { x: number; y: number; w: number; h: number }]>;
    headerAction?: Snippet;
  } = $props();

  let computed = $derived(computeMonthLayout(items, options, !!label));
</script>

<!-- content-visibility:auto on the chunk itself: when the chunk is far
     outside the viewport (per the ::view-transition-bound 50% margin
     rule), Chrome skips its descendant layout/paint cost entirely.
     contain-intrinsic-size sources from the precomputed
     intrinsicHeight so the scroll container can reserve the right
     amount of space without measuring the children — otherwise the
     chunk would collapse to 0 height when skipped, which would break
     scroll restoration and the YearScrubber. -->
<section
  class="month"
  style="min-height: {computed.intrinsicHeight}px;
         content-visibility: auto;
         contain-intrinsic-size: auto {computed.intrinsicHeight}px;"
>
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
                   width: {cell.width}px; height: {row.height}px;
                   content-visibility: auto;
                   contain-intrinsic-size: {cell.width}px {row.height}px;"
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

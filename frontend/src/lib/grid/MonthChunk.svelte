<script module lang="ts">
  import { computeJustified, type LayoutOptions } from "./justifiedLayout";

  export type MediaLite = { id: string; aspect: number; thumbUrl?: string };

  export function computeMonthLayout(items: MediaLite[], opts: LayoutOptions) {
    const layout = computeJustified(items.map((m) => ({ aspect: m.aspect })), opts);
    return { layout, intrinsicHeight: layout.totalHeight };
  }
</script>

<script lang="ts">
  import type { Snippet } from "svelte";

  let { items, options, label, renderCell }: {
    items: MediaLite[];
    options: LayoutOptions;
    label?: string;
    renderCell?: Snippet<[MediaLite, { x: number; y: number; w: number; h: number }]>;
  } = $props();

  let computed = $derived(computeMonthLayout(items, options));
</script>

<section class="month" style="min-height: {computed.intrinsicHeight}px;">
  {#if label}<header class="day-header">{label}</header>{/if}
  <div class="cells" style="position: relative; height: {computed.intrinsicHeight}px;">
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
    color: var(--text-secondary);
    padding: 16px 4px 8px;
    font-weight: 500;
  }
  .placeholder {
    width: 100%;
    height: 100%;
    background: var(--bg-elevated);
    border-radius: 2px;
  }
</style>

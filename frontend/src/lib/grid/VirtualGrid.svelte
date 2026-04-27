<script lang="ts">
  import MonthChunk, { type MediaLite } from "./MonthChunk.svelte";
  import type { Month, Media } from "../media/mediaStore.svelte";
  import { selection } from "../selection/selectionStore.svelte";

  let { months, onLoadMore, targetRowHeight = 200 }: {
    months: Month[];
    onLoadMore?: () => void;
    targetRowHeight?: number;
  } = $props();

  let containerEl: HTMLDivElement | null = $state(null);
  let containerWidth = $state(800);
  let sentinel: HTMLDivElement | null = $state(null);

  $effect(() => {
    if (!containerEl) return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width;
      if (w) containerWidth = w;
    });
    ro.observe(containerEl);
    return () => ro.disconnect();
  });

  $effect(() => {
    if (!sentinel) return;
    const io = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) onLoadMore?.();
    }, { rootMargin: "800px 0px" });
    io.observe(sentinel);
    return () => io.disconnect();
  });

  function toLite(items: Media[]): MediaLite[] {
    return items.map((m) => ({ id: m.id, aspect: m.aspect, thumbUrl: m.thumbUrl }));
  }

  // Memoize the flattened id list so shift-click doesn't re-allocate
  // O(n) strings + array on every click. Recomputes only when months
  // (the prop) changes.
  const orderedIds = $derived(months.flatMap((m) => m.items.map((it) => it.id)));

  function handleCellClick(e: MouseEvent, id: string) {
    // Ignore middle-click (button 1, opens new tab) and right-click
    // (button 2, context menu). Shift+middle-click would otherwise
    // hijack the new-tab gesture.
    if (e.button !== 0) return;
    if (e.shiftKey) {
      e.preventDefault();
      selection.range(id, orderedIds);
    } else if (e.metaKey || e.ctrlKey) {
      e.preventDefault();
      selection.toggle(id);
    }
    // else: let the anchor navigate normally
  }
</script>

<div bind:this={containerEl} class="grid">
  {#each months as month (month.key)}
    <MonthChunk
      items={toLite(month.items)}
      label={month.key}
      options={{ containerWidth, targetRowHeight, gap: 4 }}
    >
      {#snippet renderCell(m)}
        <a
          href={`/media/${m.id}`}
          aria-label={`Photo ${m.id}`}
          class:selected={selection.ids.has(m.id)}
          onclick={(e) => handleCellClick(e, m.id)}
        >
          <img src={m.thumbUrl} alt="" loading="lazy" decoding="async" style="width:100%;height:100%;object-fit:cover" />
        </a>
      {/snippet}
    </MonthChunk>
  {/each}
  <div bind:this={sentinel} style="height:1px"></div>
</div>

<style>
  .grid { padding: 8px; }
  .grid :global(a.selected) {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
    border-radius: 2px;
  }
</style>

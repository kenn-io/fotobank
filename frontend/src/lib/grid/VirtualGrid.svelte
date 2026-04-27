<script lang="ts">
  import MonthChunk from "./MonthChunk.svelte";
  import type { MediaLite } from "./monthChunkLayout";
  import StickyMonthBar from "../components/StickyMonthBar.svelte";
  import YearScrubber from "../components/YearScrubber.svelte";
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
  let activeMonth = $state<string>("");

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

  // Find the nearest scrolling ancestor so IntersectionObserver
  // measures intersection against THAT box rather than the layout
  // viewport. With the default root the rootMargin sliver lands on
  // the viewport top, but our scroll container is .main in
  // ThreeColumnLayout (overflow: auto). The sticky bar pins to .main,
  // so the observer must agree on the same reference.
  function findScrollParent(el: Element): Element | null {
    let node: Element | null = el.parentElement;
    while (node) {
      const overflowY = getComputedStyle(node).overflowY;
      if (overflowY === "auto" || overflowY === "scroll") return node;
      node = node.parentElement;
    }
    return null;
  }

  // Track which month is pinned to the top of the scroll viewport so the
  // sticky bar always reflects the chunk currently under the bar. The
  // rootMargin slices a 1px sliver at the very top: only the chunk whose
  // wrapper is currently crossing that line counts as "active".
  // Re-runs when the months array changes (length/keys read forces a
  // tracked dep) so newly mounted chunks are observed and removed ones
  // are unobserved. When no chunk is in the sliver (fast scroll, gap
  // between chunks) activeMonth retains its last value so the bar
  // doesn't blank.
  $effect(() => {
    if (!containerEl) return;
    // Read months so Svelte tracks it as a dep — the body uses
    // querySelectorAll, not the array, so we need the explicit read.
    months.length;
    const root = findScrollParent(containerEl);
    const io = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) {
          const key = entry.target.getAttribute("data-month");
          if (key) activeMonth = key;
        }
      }
    }, { root, rootMargin: "-1px 0px -100% 0px" });
    containerEl.querySelectorAll<HTMLElement>("[data-month]").forEach((el) => io.observe(el));
    return () => io.disconnect();
  });

  function toLite(items: Media[]): MediaLite[] {
    return items.map((m) => ({ id: m.id, aspect: m.aspect, thumbUrl: m.thumbUrl }));
  }

  // Memoize the flattened id list so shift-click doesn't re-allocate
  // O(n) strings + array on every click. Recomputes only when months
  // (the prop) changes.
  const orderedIds = $derived(months.flatMap((m) => m.items.map((it) => it.id)));

  function jumpTo(key: string) {
    if (!containerEl) return;
    const target = containerEl.querySelector(`[data-month="${CSS.escape(key)}"]`);
    target?.scrollIntoView({ behavior: "smooth", block: "start" });
  }

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
  <StickyMonthBar label={activeMonth} />
  <YearScrubber {months} onJump={jumpTo} />
  {#each months as month (month.key)}
    <div data-month={month.key}>
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
    </div>
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

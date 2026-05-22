<script lang="ts">
  import type { Snippet } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import MonthChunk from "./MonthChunk.svelte";
  import type { MediaLite } from "./monthChunkLayout";
  import StickyMonthBar from "../components/StickyMonthBar.svelte";
  import YearScrubber from "../components/YearScrubber.svelte";
  import type { Month, Media } from "../media/mediaStore.svelte";
  import { selection } from "../selection/selectionStore.svelte";
  import MediaCell from "./MediaCell.svelte";
  import { router } from "../router/router.svelte";

  // timelineChrome=false drops the sticky month bar, year scrubber,
  // and per-chunk day-header — the "flat" mode used by AlbumDetail
  // where dates aren't a meaningful axis. headerAction forwards a
  // per-month snippet (e.g. Task 7's GroupSelectButton) into each
  // MonthChunk's day-header in timeline mode.
  // cellOverlay forwards a per-cell snippet that renders ON TOP of
  // each MediaCell — the search page passes a DiagnosticsBadge slot
  // here; other callers omit it and the cell renders bare. The overlay
  // is positioned absolutely inside the cell's host div so it doesn't
  // disturb the justified layout.
  let {
    months, onLoadMore, targetRowHeight = 200, timelineChrome = true,
    headerAction, onOpenMedia, cellOverlay,
  }: {
    months: Month[];
    onLoadMore?: () => void;
    targetRowHeight?: number;
    timelineChrome?: boolean;
    headerAction?: Snippet<[Month]>;
    onOpenMedia?: (id: string) => void;
    cellOverlay?: Snippet<[Media]>;
  } = $props();

  let containerEl: HTMLDivElement | null = $state(null);
  let containerWidth = $state(800);
  let sentinel: HTMLDivElement | null = $state(null);
  let activeMonth = $state<string>("");
  // outOfWindowKeys holds month keys whose chunks are confirmed to
  // be outside the windowing IO's rootMargin. Default is empty so
  // every chunk renders during initial paint; the IO callback below
  // adds keys as chunks scroll out of the buffer and removes them as
  // they scroll back in. MonthChunk reads `!outOfWindowKeys.has(key)`
  // via the inWindow prop. SvelteSet from svelte/reactivity (NOT a
  // bare Set wrapped in $state) — Svelte 5's runes proxy doesn't
  // intercept Set/Map mutations, so .add/.delete on a plain Set won't
  // trigger re-renders.
  const outOfWindowKeys = new SvelteSet<string>();

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
    // Direct-child scope: a slotted headerAction or cellOverlay
    // snippet could legitimately carry data-month for its own
    // purposes; observing a descendant would corrupt the active-month
    // signal as the descendant scrolls in/out independently of its
    // wrapper.
    containerEl
      .querySelectorAll<HTMLElement>(":scope > [data-month]")
      .forEach((el) => io.observe(el));
    return () => io.disconnect();
  });

  // Windowed-mounting IO: observe each [data-month] wrapper against
  // the scroll container with a generous rootMargin (±2 viewports).
  // Chunks far outside this buffer get added to outOfWindowKeys so
  // MonthChunk.inWindow flips false and the cells unmount. The
  // chunk's wrapper section keeps its min-height so scroll math is
  // preserved. Re-runs on months.length change so newly added chunks
  // are observed and removed ones are unobserved.
  //
  // Why a Set instead of a Map<key, isInWindow>: the default for an
  // unobserved chunk should be "render" so initial paint and tests
  // that mount the component without a real scroll container work
  // the way the previous code did. Out-of-window is the explicit,
  // observed state; everything else is in-window by absence.
  $effect(() => {
    if (!containerEl) return;
    months.length;
    const root = findScrollParent(containerEl);
    const io = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        const key = entry.target.getAttribute("data-month");
        if (!key) continue;
        if (entry.isIntersecting) {
          outOfWindowKeys.delete(key);
        } else {
          outOfWindowKeys.add(key);
        }
      }
    }, { root, rootMargin: "200% 0px" });
    // Direct-child scope: see the active-month observer above for why
    // the descendant-matching selector would be wrong here.
    containerEl
      .querySelectorAll<HTMLElement>(":scope > [data-month]")
      .forEach((el) => io.observe(el));
    return () => io.disconnect();
  });

  function toLite(items: Media[]): MediaLite[] {
    return items.map((m) => ({
      id: m.id,
      aspect: m.aspect,
      thumbUrl: m.thumbUrl,
      thumbStatus: m.thumbStatus,
    }));
  }

  // findFullMedia maps a MediaLite (the layout-sliced subset) back to
  // the full Media row by id. Used by the cellOverlay snippet so the
  // overlay can read fields beyond MediaLite's three (id/aspect/
  // thumbUrl). Falls back to a tombstone Media when the lookup misses
  // — the caller's snippet runs but reads no overlay-specific fields,
  // which is the v2 search-grid fast path: cells without
  // score_components render no badge.
  function findFullMedia(items: Media[], id: string): Media {
    const found = items.find((m) => m.id === id);
    if (found) return found;
    return { id, timestamp: "", aspect: 1, thumbUrl: "", thumbStatus: "pending", taken: new Date(0), thumbVersion: 0 };
  }

  // Memoize the flattened id list so shift-click doesn't re-allocate
  // O(n) strings + array on every click. Recomputes only when months
  // (the prop) changes.
  const orderedIds = $derived(months.flatMap((m) => m.items.map((it) => it.id)));

  function jumpTo(key: string) {
    if (!containerEl) return;
    // :scope > matches a direct-child wrapper only — see the IO
    // observers above for why a descendant match would be wrong.
    const target = containerEl.querySelector(`:scope > [data-month="${CSS.escape(key)}"]`);
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
      return;
    }
    if (e.metaKey || e.ctrlKey) {
      e.preventDefault();
      selection.toggle(id);
      return;
    }
    // Plain click. Default: SPA-route to /media/:id. Source routes that
    // want lightbox behavior pass onOpenMedia which captures the
    // LightboxSession snapshot and navigates with ?from=...; the default
    // path is unchanged for any caller without the prop.
    e.preventDefault();
    if (onOpenMedia !== undefined) {
      onOpenMedia(id);
    } else {
      router.navigate(`/media/${id}`);
    }
  }
</script>

<div bind:this={containerEl} class="grid">
  {#if timelineChrome}
    <StickyMonthBar label={activeMonth} />
    <YearScrubber {months} onJump={jumpTo} />
  {/if}
  {#each months as month (month.key)}
    {@const inWindow = !outOfWindowKeys.has(month.key)}
    <div data-month={month.key}>
      {#if timelineChrome}
        {#if headerAction}
          <!-- Capture the prop into a non-shadowed local; inside the
               `{#snippet headerAction()}` body, the name `headerAction`
               binds to the snippet itself, not the prop. -->
          {@const action = headerAction}
          {@const items = month.items}
          <MonthChunk
            items={toLite(items)}
            label={month.key}
            options={{ containerWidth, targetRowHeight, gap: 4 }}
            {inWindow}
          >
            {#snippet headerAction()}
              {@render action(month)}
            {/snippet}
            {#snippet renderCell(m)}
              <div class="cell-host">
                <MediaCell
                  media={m}
                  selected={selection.ids.has(m.id)}
                  onCellClick={(e) => handleCellClick(e, m.id)}
                />
                {#if cellOverlay}
                  <div class="cell-overlay">{@render cellOverlay(findFullMedia(items, m.id))}</div>
                {/if}
              </div>
            {/snippet}
          </MonthChunk>
        {:else}
          {@const items = month.items}
          <MonthChunk
            items={toLite(items)}
            label={month.key}
            options={{ containerWidth, targetRowHeight, gap: 4 }}
            {inWindow}
          >
            {#snippet renderCell(m)}
              <div class="cell-host">
                <MediaCell
                  media={m}
                  selected={selection.ids.has(m.id)}
                  onCellClick={(e) => handleCellClick(e, m.id)}
                />
                {#if cellOverlay}
                  <div class="cell-overlay">{@render cellOverlay(findFullMedia(items, m.id))}</div>
                {/if}
              </div>
            {/snippet}
          </MonthChunk>
        {/if}
      {:else}
        <!-- Flat mode: omit `label` entirely (exactOptionalPropertyTypes
             rejects label={undefined}) so MonthChunk skips the
             day-header altogether, and don't forward headerAction
             since there's no header to mount it on. -->
        {@const items = month.items}
        <MonthChunk
          items={toLite(items)}
          options={{ containerWidth, targetRowHeight, gap: 4 }}
          {inWindow}
        >
          {#snippet renderCell(m)}
            <div class="cell-host">
              <MediaCell
                media={m}
                selected={selection.ids.has(m.id)}
                onCellClick={(e) => handleCellClick(e, m.id)}
              />
              {#if cellOverlay}
                <div class="cell-overlay">{@render cellOverlay(findFullMedia(items, m.id))}</div>
              {/if}
            </div>
          {/snippet}
        </MonthChunk>
      {/if}
    </div>
  {/each}
  <div bind:this={sentinel} style="height:1px"></div>
</div>

<style>
  .grid { padding: 8px; }
  .cell-host { position: relative; width: 100%; height: 100%; }
  .cell-overlay {
    position: absolute;
    bottom: 4px;
    right: 4px;
    pointer-events: none;
    z-index: 1;
  }
  .cell-overlay :global(*) { pointer-events: auto; }
</style>

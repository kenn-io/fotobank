<!-- frontend/src/lib/components/YearScrubber.svelte -->
<script lang="ts">
  import type { Month } from "../media/mediaStore.svelte";
  let { months, onJump }: { months: Month[]; onJump: (key: string) => void } = $props();
  let years = $derived(uniqueYears(months));

  function uniqueYears(ms: Month[]): { year: string; firstMonthKey: string }[] {
    const seen = new Map<string, string>();
    for (const m of ms) {
      const y = m.key.slice(0, 4);
      if (!seen.has(y)) seen.set(y, m.key);
    }
    return [...seen.entries()].map(([year, firstMonthKey]) => ({ year, firstMonthKey }));
  }
</script>

<aside class="scrubber" aria-label="Jump to year">
  {#each years as y (y.year)}
    <button onclick={() => onJump(y.firstMonthKey)}>{y.year}</button>
  {/each}
</aside>

<style>
  .scrubber {
    position: fixed;
    /* Sits BELOW the route header (~32px DensityControl strip) and
       the AppHeader (46px), with a gap for breathing room. */
    top: 96px;
    /* Lives inside the .main rail (ThreeColumnLayout's --rail-width =
       64px padding-right on .main). scrollbar-gutter:stable on .main
       reserves ~17px at the inline-end for the OS scrollbar, so the
       scrubber must sit *right of* that 17px window or it visibly
       overlaps the scrollbar. The mono-spaced year ticks are ~26px
       wide; centering them in the (64-17)=47px usable rail places
       them ~24px from the viewport edge — clear of the scrollbar
       on every platform that always paints one. */
    right: 24px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    z-index: 6;
    /* Subtle dotted timeline rule on the LEFT edge of the rail so
       the year ticks read as a vertical timeline rather than a
       floating list. The dots sit at low opacity so they don't
       compete with the photo grid; the year-tick buttons have a
       solid surface bg to "punch through" the dots when they sit on
       top of the line. */
    padding: 6px 4px;
    background-image: linear-gradient(
      to bottom,
      transparent 0,
      transparent 4px,
      var(--ink-4) 4px,
      var(--ink-4) 6px,
      transparent 6px,
      transparent 10px
    );
    background-size: 1px 10px;
    background-repeat: repeat-y;
    background-position: 4px 0;
  }
  .scrubber button {
    background: var(--bg);
    border: none;
    color: var(--ink-3);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.04em;
    padding: 2px 6px;
    cursor: pointer;
    border-radius: 3px;
    /* Tabular nums so 2024 / 2025 / 2026 stack with identical
       column widths; the rail reads as a precise scale. */
    font-variant-numeric: tabular-nums;
    transition: color 120ms, background 120ms;
  }
  .scrubber button:hover {
    background: var(--surface-2);
    color: var(--amber);
  }
  .scrubber button:focus-visible {
    outline: 1px solid var(--amber);
    outline-offset: 1px;
    color: var(--amber);
  }
</style>

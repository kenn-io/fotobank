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
    top: 60px;
    right: 4px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    z-index: 6;
  }
  .scrubber button {
    background: transparent;
    border: none;
    color: var(--ink-3);
    font-size: 10px;
    padding: 1px 6px;
    cursor: pointer;
    border-radius: 6px;
  }
  .scrubber button:hover { background: var(--surface-2); color: var(--ink); }
</style>

<!-- frontend/src/lib/components/DensityControl.svelte -->
<script lang="ts">
  import type { DensityStore, Preset } from "../density/densityStore.svelte";
  import { isEditableTarget } from "../dom/editable";
  let { store }: { store: DensityStore } = $props();
  const presets: Preset[] = ["compact", "comfortable", "large"];

  $effect(() => {
    function onKey(e: KeyboardEvent) {
      if (isEditableTarget(e.target)) return;
      // Skip when a modifier is held so the browser's ⌘+/⌘- (and
      // Ctrl+/Ctrl-) page-zoom shortcuts pass through unmolested.
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === "+" || e.key === "=") {
        store.nudge(1);
        e.preventDefault();
      } else if (e.key === "-" || e.key === "_") {
        store.nudge(-1);
        e.preventDefault();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });
</script>

<div class="density">
  {#each presets as p (p)}
    <button class:active={store.preset === p} onclick={() => store.set(p)}>{p}</button>
  {/each}
</div>

<style>
  .density {
    display: inline-flex;
    gap: 0;
    border: 1px solid var(--border);
    overflow: hidden;
  }
  .density button {
    background: transparent;
    border: none;
    padding: 4px 10px;
    color: var(--ink-2);
    font-size: 12px;
    cursor: pointer;
  }
  .density button.active {
    background: var(--amber);
    /* Dark foreground on the amber pill — `var(--ink)` is the light
       body color and pairs poorly with the warm-orange background.
       Using `var(--bg)` (the dark canvas) gives the active label
       enough contrast to remain readable. */
    color: var(--bg);
  }
</style>

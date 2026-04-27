<!-- frontend/src/lib/components/DensityControl.svelte -->
<script lang="ts">
  import type { DensityStore, Preset } from "../density/densityStore.svelte";
  import { isEditableTarget } from "../dom/editable";
  let { store }: { store: DensityStore } = $props();
  const presets: Preset[] = ["compact", "comfortable", "large"];

  $effect(() => {
    function onKey(e: KeyboardEvent) {
      if (isEditableTarget(e.target)) return;
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
    border-radius: var(--radius);
    overflow: hidden;
  }
  .density button {
    background: transparent;
    border: none;
    padding: 4px 10px;
    color: var(--text-secondary);
    font-size: 12px;
    cursor: pointer;
  }
  .density button.active {
    background: var(--accent);
    color: white;
  }
</style>

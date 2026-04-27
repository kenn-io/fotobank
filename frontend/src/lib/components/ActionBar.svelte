<!-- frontend/src/lib/components/ActionBar.svelte -->
<script lang="ts">
  import type { SelectionStore } from "../selection/selectionStore.svelte";
  let { selection }: { selection: SelectionStore } = $props();

  function unimplemented(label: string) {
    alert(`${label} arrives in a later sub-plan.`);
  }
</script>

{#if selection.ids.size > 0}
  <div class="bar" role="toolbar" aria-label="Selection actions">
    <span class="count">{selection.ids.size} selected</span>
    <button onclick={() => unimplemented("Add to album (F6)")}>Add to album</button>
    <button onclick={() => unimplemented("Hide (F5)")}>Hide</button>
    <button onclick={() => unimplemented("Share (F6)")}>Share</button>
    <button onclick={() => selection.clear()}>Done</button>
  </div>
{/if}

<style>
  .bar {
    position: fixed;
    top: 8px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 10;
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 6px 12px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 18px;
    box-shadow: var(--shadow);
  }
  .count { font-size: 12px; color: var(--text-secondary); padding: 0 6px; }
  button {
    background: transparent;
    border: none;
    color: var(--text-primary);
    cursor: pointer;
    font-size: 13px;
    padding: 4px 10px;
    border-radius: 12px;
  }
  button:hover { background: var(--bg-surface); }
</style>

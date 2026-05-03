<script lang="ts">
  import { selection } from "../selection/selectionStore.svelte";
  let { ids, label }: { ids: string[]; label: string } = $props();
  const allSelected = $derived(selection.hasAll(ids));
  function toggle() {
    if (allSelected) {
      selection.removeAll(ids);
    } else {
      selection.addAll(ids);
    }
  }
</script>

<button
  type="button"
  class="group-select"
  onclick={toggle}
  aria-label={allSelected
    ? `Deselect ${ids.length} photos in ${label}`
    : `Select ${ids.length} photos in ${label}`}
>
  {allSelected ? "Deselect group" : "Select group"}
</button>

<style>
  .group-select {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--ink-3);
    padding: 2px 8px;
    font-size: 11px;
    border-radius: 4px;
    cursor: pointer;
  }
  .group-select:hover {
    color: var(--ink);
    border-color: var(--ink-3);
  }
  .group-select:focus-visible {
    outline: 2px solid var(--amber);
    outline-offset: 2px;
  }
</style>

<!-- frontend/src/lib/components/ActionBar.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";
  import type { SelectionStore } from "../selection/selectionStore.svelte";

  // selectedCount overrides selection.ids.size for routes that scope
  // selection (e.g. AlbumDetail, where only album-member ids count).
  // Default to selection.ids.size for the global Library/Sessions case.
  let { selection, actions, selectedCount }: {
    selection: SelectionStore;
    actions?: Snippet;
    selectedCount?: number;
  } = $props();

  const count = $derived(selectedCount ?? selection.ids.size);
</script>

{#if count > 0}
  <div class="action-bar" role="toolbar" aria-label="Selection actions">
    <span class="count">{count} selected</span>
    {#if actions}
      <span class="actions">{@render actions()}</span>
    {/if}
    <button type="button" onclick={() => selection.clear()}>Done</button>
  </div>
{/if}

<style>
  .action-bar {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 12px;
    background: var(--bg-elevated);
    border-bottom: 1px solid var(--border);
  }
  .count { font-weight: 600; }
  .actions { display: flex; gap: 8px; }
</style>

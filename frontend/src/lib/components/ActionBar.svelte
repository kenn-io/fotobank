<!-- frontend/src/lib/components/ActionBar.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { Button } from "@kenn-io/kit-ui";
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
    <Button onclick={() => selection.clear()}>Done</Button>
  </div>
{/if}

<style>
  .action-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px;
    padding: 8px 12px;
    background: var(--bg-inset);
    border-bottom: 1px solid var(--border-default);
  }
  .count { font-weight: 600; }
  .actions { display: flex; flex-wrap: wrap; min-width: 0; gap: 8px; }
  @media (max-width: 760px) {
    .action-bar { --kit-control-height: 44px; }
  }
</style>

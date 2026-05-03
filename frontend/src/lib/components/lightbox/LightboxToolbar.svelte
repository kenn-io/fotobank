<!-- frontend/src/lib/components/lightbox/LightboxToolbar.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";

  let {
    onClose,
    onToggleInfo,
    actions,
  }: {
    onClose: () => void;
    onToggleInfo?: () => void;
    actions?: Snippet;
  } = $props();
</script>

<header class="lb-toolbar">
  <div class="lb-toolbar-left">
    <button type="button" class="lb-btn" aria-label="Close" onclick={onClose}>×</button>
  </div>
  <div class="lb-toolbar-right">
    {#if onToggleInfo}
      <button type="button" class="lb-btn" aria-label="Toggle info" onclick={onToggleInfo}>i</button>
    {/if}
    {#if actions}{@render actions()}{/if}
  </div>
</header>

<style>
  .lb-toolbar {
    position: absolute; top: 0; left: 0; right: 0;
    display: flex; justify-content: space-between; align-items: center;
    padding: var(--space-5) var(--space-6);
    color: var(--text-primary);
  }
  .lb-toolbar-left, .lb-toolbar-right {
    display: flex; gap: var(--space-4); align-items: center;
  }
  .lb-btn {
    background: var(--bg-elevated);
    color: var(--text-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: var(--space-3) var(--space-5);
    cursor: pointer;
    font-size: var(--text-lg);
  }
  .lb-btn:hover { background: var(--bg-hover); }
</style>

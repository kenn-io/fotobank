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
    padding: 12px 16px;
    color: white;
  }
  .lb-toolbar-left, .lb-toolbar-right { display: flex; gap: 8px; align-items: center; }
  .lb-btn {
    background: rgba(0,0,0,0.5); color: white; border: 1px solid rgba(255,255,255,0.2);
    border-radius: 4px; padding: 6px 12px; cursor: pointer; font-size: 16px;
  }
  .lb-btn:hover { background: rgba(0,0,0,0.7); }
</style>

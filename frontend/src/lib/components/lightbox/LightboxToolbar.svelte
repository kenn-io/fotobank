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
    <button type="button" class="lb-btn lb-btn--close" aria-label="Close" onclick={onClose}>×</button>
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
    /* Sit above the photo. panzoom applies position+transform to the
       <img>, which makes it a positioned sibling with z-index:auto;
       among positioned-auto siblings, paint follows DOM order, so an
       image with aspect wider than the stage (filling the stage's full
       height) would otherwise paint over the toolbar at the top edge. */
    z-index: 2;
  }
  .lb-toolbar-left, .lb-toolbar-right {
    display: flex; gap: var(--space-4); align-items: center;
  }
  .lb-btn {
    background: var(--bg-surface);
    color: var(--text-secondary);
    border: 1px solid var(--border-default);
    padding: var(--space-3) var(--space-5);
    cursor: pointer;
    font-size: var(--font-size-lg);
  }
  .lb-btn:hover {
    background: var(--bg-inset);
    color: var(--text-primary);
    border-color: var(--border-muted);
  }
  /* Close glyph tinted amber per the darkroom mockup; hover lifts to
     amber-deep so the affordance reads as primary action. */
  .lb-btn--close { color: var(--accent-blue); }
  .lb-btn--close:hover { color: var(--fb-accent-deep); }
</style>

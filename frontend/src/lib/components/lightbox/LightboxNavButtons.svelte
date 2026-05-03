<!-- frontend/src/lib/components/lightbox/LightboxNavButtons.svelte -->
<script lang="ts">
  let {
    hasPrev,
    hasNext,
    onPrev,
    onNext,
  }: {
    hasPrev: boolean;
    hasNext: boolean;
    onPrev: () => void;
    onNext: () => void;
  } = $props();
</script>

{#if hasPrev}
  <button type="button" class="lb-nav lb-prev" aria-label="Previous photo" onclick={onPrev}>‹</button>
{/if}
{#if hasNext}
  <button type="button" class="lb-nav lb-next" aria-label="Next photo" onclick={onNext}>›</button>
{/if}

<style>
  .lb-nav {
    position: absolute; top: 50%; transform: translateY(-50%);
    background: var(--bg-elevated);
    color: var(--text-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    width: 48px; height: 48px;
    display: flex; align-items: center; justify-content: center;
    /* 28px chevron keeps the existing tap-target weight; --text-lg (16px) reads too small. */
    font-size: 28px; cursor: pointer; padding: 0;
  }
  .lb-prev { left: var(--space-6); }
  .lb-next { right: var(--space-6); }
  .lb-nav:hover { background: var(--bg-hover); }
</style>

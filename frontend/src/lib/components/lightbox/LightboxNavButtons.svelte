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
    background: rgba(0,0,0,0.5); color: white; border: 1px solid rgba(255,255,255,0.2);
    border-radius: 50%; width: 48px; height: 48px;
    display: flex; align-items: center; justify-content: center;
    font-size: 28px; cursor: pointer; padding: 0;
  }
  .lb-prev { left: 16px; }
  .lb-next { right: 16px; }
  .lb-nav:hover { background: rgba(0,0,0,0.75); }
</style>

<!-- frontend/src/lib/components/lightbox/LightboxFrame.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";

  let {
    mode = "full",
    onBackdropClick,
    children,
  }: {
    mode?: "full" | "fallback";
    onBackdropClick?: (e: MouseEvent) => void;
    children: Snippet;
  } = $props();

  function onClick(e: MouseEvent) {
    if (e.target === e.currentTarget && onBackdropClick) onBackdropClick(e);
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="lb-backdrop" class:fallback={mode === "fallback"} role="presentation" onclick={onClick}>
  {@render children()}
</div>

<style>
  .lb-backdrop {
    position: fixed; inset: 0;
    background: var(--bg-overlay);
    z-index: 150;
    display: flex; align-items: center; justify-content: center;
  }
  .lb-backdrop.fallback { background: var(--bg-overlay); }
</style>

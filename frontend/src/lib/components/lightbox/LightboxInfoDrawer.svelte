<!-- frontend/src/lib/components/lightbox/LightboxInfoDrawer.svelte -->
<script lang="ts">
  import type { Media } from "../../media/mediaStore.svelte";
  import type { SearchScoreComponents } from "../../search/types";
  import LightboxMetadata from "./LightboxMetadata.svelte";

  // scoreComponents passes the per-media diagnostics payload through
  // to LightboxMetadata. Only the search-context lightbox supplies it;
  // omitting the prop (every non-search caller) keeps the metadata
  // panel unchanged.
  let { media, scoreComponents, onClose }: {
    media: Media;
    scoreComponents?: SearchScoreComponents;
    onClose: () => void;
  } = $props();
</script>

<aside class="lb-drawer" aria-label="Photo info">
  <header class="lb-drawer-head">
    <button type="button" aria-label="Close info" onclick={onClose}>×</button>
  </header>
  <div class="lb-drawer-body">
    {#if scoreComponents}
      <LightboxMetadata {media} {scoreComponents} />
    {:else}
      <LightboxMetadata {media} />
    {/if}
  </div>
</aside>

<style>
  /* Fills the LightboxFrame's drawer column (CSS grid track sized
     to --lb-drawer-width). The previous absolute-positioned variant
     overlaid the photo even at large window sizes; the column-based
     layout reflows the stage so the image stays clear of the drawer. */
  .lb-drawer {
    width: 100%;
    height: 100%;
    background: var(--bg-surface);
    border-left: 1px solid var(--border-default);
    display: flex; flex-direction: column;
    box-shadow: inset 0 1px 0 var(--fb-rim-highlight);
  }
  .lb-drawer-head {
    display: flex; justify-content: flex-end;
    padding: var(--space-4);
    border-bottom: 1px solid var(--border-default);
  }
  .lb-drawer-head button {
    background: transparent;
    color: var(--text-secondary);
    border: none;
    font-size: var(--font-size-lg);
    cursor: pointer;
  }
  .lb-drawer-head button:hover { color: var(--text-primary); }
  /* Drawer inherits theme colors; the metadata content reads against
     --bg-surface using the standard text tokens. */
  .lb-drawer-body {
    padding: var(--space-6);
    overflow: auto;
    color: var(--text-primary);
  }
  .lb-drawer-body :global(a) { color: var(--text-primary); }
</style>

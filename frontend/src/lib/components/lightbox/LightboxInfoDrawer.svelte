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
  .lb-drawer {
    position: absolute; top: 0; right: 0; bottom: 0;
    width: min(360px, 40vw);
    background: var(--surface);
    border-left: 1px solid var(--border-2);
    display: flex; flex-direction: column;
  }
  .lb-drawer-head {
    display: flex; justify-content: flex-end;
    padding: var(--space-4);
    border-bottom: 1px solid var(--border);
  }
  .lb-drawer-head button {
    background: transparent;
    color: var(--ink);
    border: none;
    font-size: var(--text-lg);
    cursor: pointer;
  }
  /* Drawer inherits theme colors; the metadata content reads against
     --bg-surface using the standard text tokens. */
  .lb-drawer-body {
    padding: var(--space-6);
    overflow: auto;
    color: var(--ink);
  }
  .lb-drawer-body :global(a) { color: var(--ink); }
</style>

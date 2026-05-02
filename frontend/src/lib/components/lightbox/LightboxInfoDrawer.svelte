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
    background: rgba(20,20,20,0.95);
    border-left: 1px solid rgba(255,255,255,0.1);
    display: flex; flex-direction: column;
  }
  .lb-drawer-head {
    display: flex; justify-content: flex-end;
    padding: 8px;
    border-bottom: 1px solid rgba(255,255,255,0.1);
  }
  .lb-drawer-head button {
    background: transparent; color: white; border: none; font-size: 18px; cursor: pointer;
  }
  /* Drawer is dark; force white text + links for the metadata
     content it wraps. The sheet variant inherits theme colors
     instead. */
  .lb-drawer-body { padding: 16px; overflow: auto; color: white; }
  .lb-drawer-body :global(a) { color: white; }
</style>

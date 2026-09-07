<!-- frontend/src/lib/components/lightbox/LightboxFrame.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";

  // The frame splits its viewport into a stage column (where the
  // image, toolbar, and nav buttons live) and an optional drawer
  // column on the right. When `drawer` is provided, the backdrop
  // becomes a CSS grid with two columns and the photo flex-centers
  // inside the shrunk stage instead of bleeding under the drawer.
  // The mobile sheet path doesn't pass `drawer` (it overlays
  // full-screen via LightboxInfoSheet's own positioning).
  let {
    mode = "full",
    onBackdropClick,
    children,
    drawer,
  }: {
    mode?: "full" | "fallback";
    onBackdropClick?: (e: MouseEvent) => void;
    children: Snippet;
    // exactOptionalPropertyTypes:true rejects `Snippet | undefined`
    // for an optional prop, so spell it explicitly. Callers pass
    // `undefined` to opt out of the drawer column without rebuilding
    // the whole props object.
    drawer?: Snippet | undefined;
  } = $props();

  // The stage covers the entire backdrop area now (it's the grid track
  // that holds the photo + toolbar + nav buttons), so clicks on the
  // empty dark margin around the photo land on .lb-stage rather than
  // .lb-backdrop. Treat self-clicks on either element as backdrop
  // clicks; child elements (the image, buttons) bubble through with a
  // different e.target and the equality check rejects them.
  function onClick(e: MouseEvent) {
    if (!onBackdropClick) return;
    if (e.target === e.currentTarget) onBackdropClick(e);
  }
</script>

<div
  class="lb-backdrop"
  class:fallback={mode === "fallback"}
  class:with-drawer={drawer !== undefined}
  data-testid="lightbox"
  role="presentation"
  onclick={onClick}
>
  <div class="lb-stage" role="presentation" onclick={onClick}>
    {@render children()}
  </div>
  {#if drawer}
    <div class="lb-drawer-slot">{@render drawer()}</div>
  {/if}
</div>

<style>
  .lb-backdrop {
    position: fixed; inset: 0;
    background: rgba(10, 10, 13, 0.92);
    z-index: 150;
    display: grid;
    grid-template-columns: 1fr;
    grid-template-rows: 1fr;
  }
  /* When a drawer is open the layout becomes a two-column grid: the
     stage flexes to fill the remaining space, the drawer column is
     a fixed track. Width is exposed as --lb-drawer-width so
     LightboxInfoDrawer can read the same source of truth. */
  .lb-backdrop.with-drawer {
    --lb-drawer-width: min(360px, 40vw);
    grid-template-columns: 1fr var(--lb-drawer-width);
  }
  .lb-stage {
    position: relative;
    /* min-width:0 lets the grid track actually shrink — without it
       a wide image would push the stage track to its own intrinsic
       size and the drawer column would be squeezed. */
    min-width: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    overflow: hidden;
  }
  .lb-drawer-slot {
    position: relative;
    overflow: hidden;
  }
</style>

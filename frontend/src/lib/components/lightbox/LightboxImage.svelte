<!-- frontend/src/lib/components/lightbox/LightboxImage.svelte -->
<!--
  Pan/zoom image viewer used inside the lightbox. Mounts the vendored
  `panzoom` library on the <img> and exposes zoom controls to the
  parent via the `onReady` callback prop.

  Pattern note: Svelte 5 runes do not permit `export function` for
  imperative APIs, so we hand a `LightboxImageApi` to the parent on
  mount. The parent stashes the api and calls e.g. api.zoomIn() from
  keyboard handlers (`+`, `-`, `0`, space).

  Lifecycle contract: `onReady` fires once per component mount. The
  Lightbox parent re-keys this component on activeId change, so a
  fresh api object arrives with each navigation. The parent must
  always use the most-recent api — closures over a stale api will
  silently no-op (api methods guard `pz`/`container`, both null'd
  by onDestroy).
-->
<script lang="ts" module>
  export type LightboxImageApi = {
    zoomIn(): void;
    zoomOut(): void;
    resetZoom(): void;
    toggleZoom(): void;
  };
</script>

<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import panzoom from "panzoom";
  import type { PanZoom } from "panzoom";

  let {
    src,
    alt,
    onError,
    onReady,
  }: {
    src: string;
    alt: string;
    onError?: (() => void) | undefined;
    onReady?: ((api: LightboxImageApi) => void) | undefined;
  } = $props();

  let imgEl: HTMLImageElement | null = $state(null);
  let container: HTMLDivElement | null = $state(null);
  let pz: PanZoom | null = null;

  onMount(() => {
    if (!imgEl) return;
    pz = panzoom(imgEl, {
      maxZoom: 8,
      minZoom: 1,
      bounds: true,
      boundsPadding: 0.1,
      smoothScroll: false,
      // Disable pan when at 1x so backdrop click + horizontal swipe
      // remain available for close + prev/next. panzoom treats a
      // truthy return as "skip default behavior" — the .d.ts types
      // these as `void`, but TS allows boolean returns in callback
      // positions where the consumer ignores the return type.
      beforeMouseDown: () => {
        if ((pz?.getTransform().scale ?? 1) <= 1) return true;
        return undefined;
      },
      // beforeWheel default = allow zoom; no override needed.
    });
    // Snap translation back to zero once the scale returns to the 1x
    // floor. Repro: wheel-zoom in, drag-pan, wheel-zoom out. panzoom
    // doesn't auto-recenter when scale hits minZoom, so the image
    // stays translated — and since beforeMouseDown blocks pan at <=1x
    // (so backdrop click + swipe-to-prev still work), the user can't
    // drag the image back into view. Detection on the `zoom` event
    // (not `transform`) avoids re-entrancy: `moveTo` emits `pan`,
    // not `zoom`, so calling moveTo here doesn't fire this handler
    // again. Epsilon (1.001) absorbs the float-add drift of long
    // wheel sequences. We only snap when the translation is actually
    // off-zero so we don't churn the transform on no-op wheel ticks.
    pz.on("zoom", () => {
      if (!pz) return;
      const t = pz.getTransform();
      if (t.scale <= 1.001 && (Math.abs(t.x) > 0.5 || Math.abs(t.y) > 0.5)) {
        pz.moveTo(0, 0);
      }
    });
    imgEl.addEventListener("dblclick", onDblClick);
    onReady?.({ zoomIn, zoomOut, resetZoom, toggleZoom });
  });

  onDestroy(() => {
    pz?.dispose();
    pz = null;
    imgEl?.removeEventListener("dblclick", onDblClick);
  });

  // Reset to scale 1 with the image re-centered. zoomAbs(_, _, 1)
  // alone keeps the existing translation, so a previously-panned image
  // would land off-center after reset; pairing with moveTo(0, 0)
  // returns the transform to its initial state and lets the
  // container's flex centering re-take over.
  function resetTransform(): void {
    if (!pz) return;
    pz.moveTo(0, 0);
    pz.zoomAbs(0, 0, 1);
  }

  function onDblClick(e: MouseEvent) {
    if (!pz) return;
    const cur = pz.getTransform().scale;
    if (cur > 1.5) resetTransform();
    else pz.zoomTo(e.clientX, e.clientY, 2);
  }

  // panzoom.zoomTo expects client/viewport coordinates as the
  // interaction point. clientWidth/Height give the container's size,
  // not its viewport position — without the bounding-rect offset the
  // zoom would center on the wrong point whenever the lightbox
  // doesn't sit at the viewport origin.
  function containerCenter(): { cx: number; cy: number } | null {
    if (!container) return null;
    const rect = container.getBoundingClientRect();
    return { cx: rect.left + rect.width / 2, cy: rect.top + rect.height / 2 };
  }

  function zoomIn(): void {
    const c = containerCenter();
    if (!pz || !c) return;
    pz.zoomTo(c.cx, c.cy, 1.25);
  }
  function zoomOut(): void {
    const c = containerCenter();
    if (!pz || !c) return;
    pz.zoomTo(c.cx, c.cy, 1 / 1.25);
  }
  function resetZoom(): void {
    resetTransform();
  }
  function toggleZoom(): void {
    const c = containerCenter();
    if (!pz || !c) return;
    const cur = pz.getTransform().scale;
    if (cur > 1.5) resetTransform();
    else pz.zoomTo(c.cx, c.cy, 2);
  }
</script>

<div class="lb-image-container" bind:this={container}>
  <!-- svelte-ignore a11y_img_redundant_alt -->
  <img
    bind:this={imgEl}
    {src}
    {alt}
    onerror={() => onError?.()}
    draggable="false"
  />
</div>

<style>
  .lb-image-container {
    width: 100%; height: 100%;
    display: flex; align-items: center; justify-content: center;
    overflow: hidden;
  }
  .lb-image-container :global(img) {
    max-width: 100%; max-height: 100%;
    object-fit: contain;
    user-select: none;
    -webkit-user-drag: none;
  }
</style>

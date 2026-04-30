<!-- frontend/src/lib/components/lightbox/LightboxImage.svelte -->
<!--
  Pan/zoom image viewer used inside the lightbox. Mounts the vendored
  `panzoom` library on the <img> and exposes zoom controls to the
  parent via the `onReady` callback prop.

  Pattern note: Svelte 5 runes do not permit `export function` for
  imperative APIs, so we hand a `LightboxImageApi` to the parent on
  mount. The parent stashes the api and calls e.g. api.zoomIn() from
  keyboard handlers (`+`, `-`, `0`, space).
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
      beforeWheel: () => undefined,
    });
    imgEl.addEventListener("dblclick", onDblClick);
    onReady?.({ zoomIn, zoomOut, resetZoom, toggleZoom });
  });

  onDestroy(() => {
    pz?.dispose();
    imgEl?.removeEventListener("dblclick", onDblClick);
  });

  function onDblClick(e: MouseEvent) {
    if (!pz) return;
    const cur = pz.getTransform().scale;
    if (cur > 1.5) pz.zoomAbs(0, 0, 1);
    else pz.zoomTo(e.clientX, e.clientY, 2);
  }

  function zoomIn(): void {
    if (!pz || !container) return;
    const cx = container.clientWidth / 2;
    const cy = container.clientHeight / 2;
    pz.zoomTo(cx, cy, 1.25);
  }
  function zoomOut(): void {
    if (!pz || !container) return;
    const cx = container.clientWidth / 2;
    const cy = container.clientHeight / 2;
    pz.zoomTo(cx, cy, 1 / 1.25);
  }
  function resetZoom(): void {
    pz?.zoomAbs(0, 0, 1);
  }
  function toggleZoom(): void {
    if (!pz || !container) return;
    const cur = pz.getTransform().scale;
    if (cur > 1.5) pz.zoomAbs(0, 0, 1);
    else pz.zoomTo(container.clientWidth / 2, container.clientHeight / 2, 2);
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

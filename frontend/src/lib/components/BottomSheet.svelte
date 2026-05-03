<!-- frontend/src/lib/components/BottomSheet.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { onMount, onDestroy } from "svelte";
  import { modalStack } from "../lightbox/modalStack.svelte";

  let {
    id,
    onClose,
    snap = "peek",
    children,
  }: {
    id: string;
    onClose: () => void;
    snap?: "peek" | "full";
    children: Snippet;
  } = $props();

  let dragStart: number | null = null;
  let dragDelta = $state(0);
  let sheetEl: HTMLDivElement | null = $state(null);
  let prevFocus: Element | null = null;

  onMount(() => {
    modalStack.push({ id, onEscape: onClose });
    // Move focus into the sheet so keyboard users land inside the
    // dialog (sheet has tabindex="-1" for this). Capture the prior
    // focus owner so onDestroy can restore it. A full focus trap is
    // a project-wide initiative — see ConfirmModal/AddToAlbumModal,
    // which currently share the same gap; tracked separately.
    prevFocus = document.activeElement;
    sheetEl?.focus();
  });
  onDestroy(() => {
    modalStack.pop(id);
    // Guard the HTMLElement global so onDestroy doesn't ReferenceError
    // in any SSR pass (Svelte 5 lifecycle can run server-side).
    if (
      prevFocus !== null
      && typeof HTMLElement !== "undefined"
      && prevFocus instanceof HTMLElement
    ) prevFocus.focus();
  });

  function onPointerDown(e: PointerEvent) {
    dragStart = e.clientY;
    dragDelta = 0;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  }
  function onPointerMove(e: PointerEvent) {
    if (dragStart === null) return;
    dragDelta = Math.max(0, e.clientY - dragStart);
  }
  function onPointerUp(e: PointerEvent) {
    const el = e.currentTarget as HTMLElement;
    // releasePointerCapture throws InvalidStateError if capture is
    // already released (browser-driven, e.g. on pointercancel).
    if (el.hasPointerCapture(e.pointerId)) el.releasePointerCapture(e.pointerId);
    // pointercancel = system-driven abort (context menu, app switch).
    // Don't dismiss in that case — only a deliberate pointerup past the
    // threshold counts as a swipe-down close.
    if (e.type !== "pointercancel" && dragDelta > 80) onClose();
    dragStart = null;
    dragDelta = 0;
  }

  function onBackdropClick(e: MouseEvent) {
    if (e.target === e.currentTarget) onClose();
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="bs-backdrop" role="presentation" onclick={onBackdropClick}>
  <div
    class="bs-sheet"
    class:full={snap === "full"}
    role="dialog"
    aria-modal="true"
    tabindex="-1"
    bind:this={sheetEl}
    style:transform={`translateY(${dragDelta}px)`}
    onclick={(e) => e.stopPropagation()}
  >
    <button
      type="button"
      class="bs-handle"
      aria-label="Drag handle"
      onpointerdown={onPointerDown}
      onpointermove={onPointerMove}
      onpointerup={onPointerUp}
      onpointercancel={onPointerUp}
    ></button>
    <div class="bs-body">{@render children()}</div>
  </div>
</div>

<style>
  .bs-backdrop {
    position: fixed; inset: 0;
    background: rgba(10, 10, 13, 0.78);
    z-index: 200;
    display: flex; align-items: flex-end; justify-content: center;
  }
  .bs-sheet {
    background: var(--surface);
    border-top: 1px solid var(--border);
    width: 100%;
    max-height: 50vh;
    display: flex; flex-direction: column;
    transition: transform 120ms ease-out, max-height 200ms ease-out;
  }
  .bs-sheet.full { max-height: 90vh; }
  .bs-handle {
    align-self: center;
    width: 40px; height: 4px;
    background: var(--border-2);
    margin: var(--space-4) 0 var(--space-5);
    border: none;
    padding: 0;
    touch-action: none;
    cursor: grab;
  }
  .bs-body { overflow: auto; padding: 0 var(--space-6) var(--space-6); }
</style>

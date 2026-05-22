<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { modalStack } from "../lightbox/modalStack.svelte";

  let {
    title,
    body,
    confirmLabel = "Confirm",
    cancelLabel = "Cancel",
    danger = false,
    onConfirm,
    onCancel,
  }: {
    title: string;
    body: string;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    onConfirm: () => Promise<void> | void;
    onCancel: () => void;
  } = $props();

  let pending = $state(false);

  const modalId = `confirm-${Math.random().toString(36).slice(2)}`;
  // onEscape returns false while pending so a later Esc can still
  // cancel the modal once the in-flight confirm settles.
  onMount(() => modalStack.push({
    id: modalId,
    onEscape: () => {
      if (pending) return false;
      onCancel();
    },
  }));
  onDestroy(() => modalStack.pop(modalId));

  async function confirm() {
    pending = true;
    try {
      await onConfirm();
    } finally {
      pending = false;
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="modal-backdrop" role="presentation" onclick={() => { if (!pending) onCancel(); }}>
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal" role="dialog" aria-modal="true" aria-label={title} tabindex="-1" onclick={(e) => e.stopPropagation()}>
    <h2>{title}</h2>
    <p>{body}</p>
    <div class="actions">
      <button type="button" class="btn-ghost" onclick={onCancel} disabled={pending}>{cancelLabel}</button>
      <button type="button" class="btn-primary" class:danger onclick={confirm} disabled={pending}>
        {pending ? "Working…" : confirmLabel}
      </button>
    </div>
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed; inset: 0;
    background: rgba(10, 10, 13, 0.78);
    display: flex; align-items: center; justify-content: center;
    /* Above the lightbox (z-index: 150) so confirm dialogs opened
       from the lightbox stack in front of the lightbox backdrop. */
    z-index: 200;
  }
  .modal {
    background: var(--surface);
    border: 1px solid var(--border);
    padding: var(--space-6);
    min-width: 320px; max-width: 480px;
    color: var(--ink);
    /* Relief — modals float above the scrim; the strong composite adds
       a soft drop plus the rim highlight so the panel reads as
       elevated, not stamped. */
    box-shadow: var(--shadow-relief-strong);
  }
  .modal h2 { margin-top: 0; }
  .actions {
    display: flex; gap: var(--space-4); justify-content: flex-end;
    margin-top: var(--space-5);
  }
  /* Ghost secondary: transparent until hover. */
  .btn-ghost {
    background: transparent;
    color: var(--ink-2);
    border: 1px solid var(--border);
    padding: var(--space-3) var(--space-5);
    cursor: pointer;
  }
  .btn-ghost:hover:not(:disabled) {
    background: var(--surface-2);
    color: var(--ink);
    border-color: var(--border-2);
  }
  .btn-ghost:disabled { cursor: not-allowed; opacity: 0.6; }
  /* Amber primary: filled accent for the confirm action. Dark
     foreground (--bg) instead of --ink so the label has readable
     contrast against the warm-orange fill. */
  .btn-primary {
    background: var(--amber);
    color: var(--bg);
    border: 1px solid var(--amber);
    padding: var(--space-3) var(--space-5);
    cursor: pointer;
    font-weight: 500;
  }
  .btn-primary:hover:not(:disabled) {
    background: var(--amber-deep);
    border-color: var(--amber-deep);
  }
  .btn-primary:disabled { cursor: not-allowed; opacity: 0.6; }
  /* Destructive variant overrides the amber primary with --danger. */
  .btn-primary.danger {
    background: var(--danger);
    border-color: var(--danger);
  }
  .btn-primary.danger:hover:not(:disabled) {
    background: var(--danger);
    border-color: var(--danger);
    filter: brightness(1.1);
  }
</style>

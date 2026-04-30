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
  onMount(() => modalStack.push({ id: modalId, onEscape: () => { if (!pending) onCancel(); } }));
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
      <button type="button" onclick={onCancel} disabled={pending}>{cancelLabel}</button>
      <button type="button" class:danger onclick={confirm} disabled={pending}>
        {pending ? "Working…" : confirmLabel}
      </button>
    </div>
  </div>
</div>

<style>
  .modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.5); display: flex; align-items: center; justify-content: center; z-index: 100; }
  .modal { background: var(--bg-elevated); border: 1px solid var(--border); border-radius: 8px; padding: 16px; min-width: 320px; max-width: 480px; }
  .modal h2 { margin-top: 0; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 12px; }
  .danger { background: var(--danger); color: white; border-color: var(--danger); }
</style>

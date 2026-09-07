<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { modalStack } from "../lightbox/modalStack.svelte";

  let {
    initialName,
    onSave,
    onCancel,
  }: {
    initialName: string;
    onSave: (name: string) => Promise<void>;
    onCancel: () => void;
  } = $props();

  // Seed once from the initial prop value; the modal owns this state
  // for its lifetime and doesn't track later parent updates.
  // svelte-ignore state_referenced_locally
  let name = $state(initialName);
  let pending = $state(false);
  let error = $state<string | null>(null);
  const trimmed = $derived(name.trim());
  const valid = $derived(trimmed.length >= 1 && trimmed.length <= 200 && trimmed !== initialName.trim());

  const modalId = `rename-${Math.random().toString(36).slice(2)}`;
  // onEscape returns false while pending so a later Esc can still
  // cancel the modal once the in-flight rename settles.
  onMount(() => modalStack.push({
    id: modalId,
    onEscape: () => {
      if (pending) return false;
      onCancel();
    },
  }));
  onDestroy(() => modalStack.pop(modalId));

  async function submit(e: Event) {
    e.preventDefault();
    if (!valid || pending) return;
    pending = true;
    error = null;
    try {
      await onSave(trimmed);
    } catch (err: unknown) {
      // huma errors come back as `{title, detail}`; inline `throw new Error(...)`
      // surfaces as `{message}`. Match NewAlbumForm's extraction order.
      const e2 = (err ?? {}) as { message?: string; detail?: string; title?: string };
      error = e2.detail ?? e2.message ?? e2.title ?? "Failed to save";
    } finally {
      pending = false;
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="modal-backdrop" role="presentation" onclick={() => { if (!pending) onCancel(); }}>
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div class="modal" role="dialog" aria-modal="true" aria-label="Rename album" tabindex="-1" onclick={(e) => e.stopPropagation()}>
    <h2>Rename album</h2>
    <form onsubmit={submit}>
      <label>
        <span>Name</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input type="text" bind:value={name} maxlength="200" autofocus />
      </label>
      {#if error}<div class="error" role="alert">{error}</div>{/if}
      <div class="actions">
        <button type="button" onclick={onCancel} disabled={pending}>Cancel</button>
        <button type="submit" disabled={!valid || pending}>{pending ? "Saving…" : "Save"}</button>
      </div>
    </form>
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,0.5);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal { background: var(--bg-inset); border: 1px solid var(--border-default); border-radius: 8px; padding: 16px; min-width: 320px; }
  .modal h2 { margin-top: 0; }
  label { display: flex; flex-direction: column; gap: 4px; }
  .error { color: var(--accent-red); font-size: 13px; margin-top: 8px; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 12px; }
</style>

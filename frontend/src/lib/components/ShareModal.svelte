<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { parseGrantee, type Grantee } from "../format/parseGrantee";
  import { modalStack } from "../lightbox/modalStack.svelte";
  import type { ShareTarget, CreateShareBody } from "../share/shareTypes";

  let {
    target,
    onCreate,
    onClose,
  }: {
    target: ShareTarget;
    onCreate: (body: CreateShareBody) => Promise<void>;
    onClose: () => void;
  } = $props();

  let granteeRaw = $state("");
  let label = $state("");
  let allowDownload = $state(false);
  let pending = $state(false);
  let error = $state<string | null>(null);

  const modalId = `share-${Math.random().toString(36).slice(2)}`;
  // onEscape returns false while pending so a later Esc can still
  // close the modal once the in-flight share request settles.
  onMount(() => modalStack.push({
    id: modalId,
    onEscape: () => {
      if (pending) return false;
      onClose();
    },
  }));
  onDestroy(() => modalStack.pop(modalId));

  const grantee: Grantee | null = $derived(parseGrantee(granteeRaw));
  const labelTrimmed = $derived(label.trim());
  const labelOk = $derived(labelTrimmed.length <= 200);
  const setTooLarge = $derived(target.type === "media_set" && target.mediaIds.length > 1000);
  // Defensive: the action bar gates opening on selectedCount > 0, so an
  // empty media_set should never reach here. Block submit anyway so a
  // bug elsewhere can't POST a zero-photo share. No UI warning — the
  // user shouldn't see this state in the first place.
  const setEmpty = $derived(target.type === "media_set" && target.mediaIds.length === 0);
  const valid = $derived(grantee !== null && labelOk && !setTooLarge && !setEmpty);

  // $derived.by(...) — multi-statement derived returning a string.
  // $derived(() => ...) would yield a function-typed value (see
  // AlbumDetail.svelte:30, AddToAlbumModal.svelte:43 precedent).
  const title: string = $derived.by((): string => {
    if (target.type === "album_live") return `Share album: ${target.albumName}`;
    const n = target.mediaIds.length;
    return `Share ${n} ${n === 1 ? "photo" : "photos"}`;
  });

  const summary: string = $derived.by((): string => {
    if (target.type === "album_live") return "Live album";
    const n = target.mediaIds.length;
    return `${n} ${n === 1 ? "photo" : "photos"} selected`;
  });

  async function submit(e: Event) {
    e.preventDefault();
    if (!valid || pending || grantee === null) return;
    pending = true;
    error = null;
    try {
      // TypeScript narrows `target.mediaIds` / `target.albumId` only
      // inside the matching `target.type` branches; build the body
      // shape inside each branch so the discriminant lines up with
      // the right payload fields.
      let body: CreateShareBody;
      if (target.type === "media_set") {
        body = {
          target_type: "media_set",
          media_ids: target.mediaIds,
          grantee,
          label: labelTrimmed,
          allow_download: allowDownload,
        };
      } else {
        body = {
          target_type: "album_live",
          album_id: target.albumId,
          grantee,
          label: labelTrimmed,
          allow_download: allowDownload,
        };
      }
      await onCreate(body);
      // Match AddToAlbumModal's close contract: dismiss on success so
      // every caller's onCreate handler can stay focused on the network
      // call + side effects (selection.clear, etc) without remembering
      // to flip a parent-owned `shareOpen` flag.
      onClose();
    } catch (err: unknown) {
      // huma errors come back as `{title, detail}`; inline thrown JS
      // Errors as `{message}`. Match NewAlbumForm's extraction order.
      const e2 = (err ?? {}) as { message?: string; detail?: string; title?: string };
      error = e2.detail ?? e2.message ?? e2.title ?? "Failed to create share";
    } finally {
      pending = false;
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="modal-backdrop" role="presentation" onclick={() => { if (!pending) onClose(); }}>
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal" role="dialog" aria-modal="true" aria-label={title} tabindex="-1" onclick={(e) => e.stopPropagation()}>
    <h2>{title}</h2>
    <div class="summary">{summary}</div>
    <form onsubmit={submit}>
      <label>
        <span>Grantee</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input
          type="text"
          class="grantee"
          bind:value={granteeRaw}
          placeholder="myhub:bob"
          autocomplete="off"
          spellcheck="false"
          aria-invalid={granteeRaw !== "" && grantee === null}
          autofocus
        />
        <span class="helper">hub:user_id</span>
      </label>
      <label>
        <span>Label</span>
        <input type="text" bind:value={label} maxlength="200" />
        <span class="helper">Helps identify this share later.</span>
      </label>
      <label class="checkbox">
        <input type="checkbox" bind:checked={allowDownload} />
        <span>Allow download</span>
      </label>
      {#if setTooLarge}
        <div class="warn" role="alert">Selection too large (max 1000 per share)</div>
      {/if}
      {#if error}<div class="error" role="alert">{error}</div>{/if}
      <div class="actions">
        <button type="button" class="btn-ghost" onclick={onClose} disabled={pending}>Cancel</button>
        <button type="submit" class="btn-primary" disabled={!valid || pending}>
          {pending ? "Creating…" : "Create share"}
        </button>
      </div>
    </form>
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed; inset: 0;
    background: rgba(10, 10, 13, 0.78);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--surface);
    border: 1px solid var(--border);
    padding: var(--space-6);
    min-width: 400px;
    max-width: 480px;
    color: var(--ink);
    /* Relief — same elevated-panel treatment as ConfirmModal. */
    box-shadow: var(--shadow-relief-strong);
  }
  .modal h2 { margin-top: 0; }
  .summary {
    color: var(--ink-3);
    font-size: var(--text-base);
    margin-bottom: var(--space-5);
  }
  form { display: flex; flex-direction: column; gap: var(--space-5); }
  label { display: flex; flex-direction: column; gap: var(--space-2); }
  label.checkbox { flex-direction: row; align-items: center; gap: var(--space-4); }
  .grantee { font-family: var(--font-mono); }
  .helper { font-size: var(--text-sm); color: var(--ink-3); }
  .warn { color: var(--warn); font-size: var(--text-base); }
  .error { color: var(--danger); font-size: var(--text-base); }
  .actions { display: flex; gap: var(--space-4); justify-content: flex-end; }
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
  .btn-primary {
    background: var(--amber);
    color: var(--ink);
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
</style>

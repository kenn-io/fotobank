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
<div class="modal-backdrop" role="presentation" onclick={() => { if (!pending) onClose(); }}>
  <!-- svelte-ignore a11y_click_events_have_key_events -->
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
    /* Above the lightbox (z-index: 150) so a Share opened from the
       lightbox stacks in front of the lightbox backdrop. */
    z-index: 200;
  }
  .modal {
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    padding: var(--space-6);
    width: min(480px, calc(100% - 2 * var(--space-4)));
    color: var(--text-primary);
    /* Relief — same elevated-panel treatment as ConfirmModal. */
    box-shadow: var(--fb-shadow-relief-strong);
  }
  .modal h2 { margin-top: 0; }
  .summary {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    margin-bottom: var(--space-5);
  }
  form { display: flex; flex-direction: column; gap: var(--space-5); }
  label { display: flex; flex-direction: column; gap: var(--space-2); }
  label.checkbox { flex-direction: row; align-items: center; gap: var(--space-4); }
  .grantee { font-family: var(--font-mono); }
  .helper { font-size: var(--font-size-xs); color: var(--text-muted); }
  .warn { color: var(--accent-amber); font-size: var(--font-size-sm); }
  .error { color: var(--accent-red); font-size: var(--font-size-sm); }
  .actions { display: flex; gap: var(--space-4); justify-content: flex-end; }
  .btn-ghost {
    background: transparent;
    color: var(--text-secondary);
    border: 1px solid var(--border-default);
    padding: var(--space-3) var(--space-5);
    cursor: pointer;
  }
  .btn-ghost:hover:not(:disabled) {
    background: var(--bg-inset);
    color: var(--text-primary);
    border-color: var(--border-muted);
  }
  .btn-ghost:disabled { cursor: not-allowed; opacity: 0.6; }
  .btn-primary {
    background: var(--accent-blue);
    /* Dark foreground on amber so the label stays readable; --text-primary
       (light body color) was too low-contrast against the warm fill. */
    color: var(--bg-primary);
    border: 1px solid var(--accent-blue);
    padding: var(--space-3) var(--space-5);
    cursor: pointer;
    font-weight: 500;
  }
  .btn-primary:hover:not(:disabled) {
    background: var(--fb-accent-deep);
    border-color: var(--fb-accent-deep);
  }
  .btn-primary:disabled { cursor: not-allowed; opacity: 0.6; }
</style>

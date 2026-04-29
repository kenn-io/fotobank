<script lang="ts">
  import { parseGrantee, type Grantee } from "../format/parseGrantee";
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

  const grantee: Grantee | null = $derived(parseGrantee(granteeRaw));
  const labelTrimmed = $derived(label.trim());
  const labelOk = $derived(labelTrimmed.length <= 200);
  const setTooLarge = $derived(target.type === "media_set" && target.mediaIds.length > 1000);
  const valid = $derived(grantee !== null && labelOk && !setTooLarge);

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

<svelte:window onkeydown={(e) => { if (e.key === "Escape" && !pending) onClose(); }} />

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
        <button type="button" onclick={onClose} disabled={pending}>Cancel</button>
        <button type="submit" disabled={!valid || pending}>
          {pending ? "Creating…" : "Create share"}
        </button>
      </div>
    </form>
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,0.5);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 16px;
    min-width: 400px;
    max-width: 480px;
  }
  .modal h2 { margin-top: 0; }
  .summary { color: var(--text-muted); font-size: 13px; margin-bottom: 12px; }
  form { display: flex; flex-direction: column; gap: 12px; }
  label { display: flex; flex-direction: column; gap: 4px; }
  label.checkbox { flex-direction: row; align-items: center; gap: 8px; }
  .grantee { font-family: monospace; }
  .helper { font-size: 12px; color: var(--text-muted); }
  .warn { color: var(--warn); font-size: 13px; }
  .error { color: var(--danger); font-size: 13px; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; }
</style>

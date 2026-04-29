<script lang="ts">
  import type {
    ScopeListRow,
    ScopeDetail,
    SharePreview,
    SharesStore,
  } from "../shares/sharesStore.svelte";
  import { handleInternalLinkClick } from "../router/router.svelte";
  import ShareStatePill from "./ShareStatePill.svelte";

  let {
    scope,
    sharesStore,
    onClose,
    onRevoke,
    onRetry,
  }: {
    scope: ScopeListRow;
    sharesStore: SharesStore;
    onClose: () => void;
    onRevoke: (uuid: string) => void;
    onRetry: (uuid: string) => void;
  } = $props();

  let detail = $state<ScopeDetail | null>(null);
  let preview = $state<SharePreview | null>(null);
  let detailLoading = $state(false);
  let previewLoading = $state(false);

  // Capture scope.uuid before await so a fast prop swap (user clicks a
  // different row mid-flight) can't land a stale response on top of the
  // newer detail. Same uuid-guard pattern as MediaDetail.svelte and
  // SharesStore's loadToken — if uuid !== scope.uuid by the time the
  // promise resolves, we drop the result and let the newer effect run.
  $effect(() => {
    const uuid = scope.uuid;
    detail = null;
    preview = null;
    detailLoading = true;
    // Reset previewLoading too: a stale getPreview() promise from the
    // previous uuid will early-return below, so without this reset the
    // Preview button would stay disabled for the new row.
    previewLoading = false;
    sharesStore.getDetail(uuid).then((d) => {
      if (uuid !== scope.uuid) return;
      detail = d;
      detailLoading = false;
    });
  });

  function granteeText(s: ScopeListRow): string {
    if (s.grantee_handle) return s.grantee_handle;
    return `${s.grantee.hub}:${s.grantee.user_id}`;
  }

  async function loadPreview() {
    const uuid = scope.uuid;
    previewLoading = true;
    const result = await sharesStore.getPreview(uuid);
    if (uuid !== scope.uuid) return;
    preview = result;
    previewLoading = false;
  }

  const summaryLabel = $derived(scope.target_summary?.label ?? "(unknown)");
</script>

<svelte:window onkeydown={(e) => { if (e.key === "Escape") onClose(); }} />

<aside class="drawer">
  <header>
    <h2>{scope.label || summaryLabel}</h2>
    <ShareStatePill {scope} />
    <button type="button" class="close" onclick={onClose} aria-label="Close drawer">×</button>
  </header>

  <dl>
    <dt>Grantee</dt>
    <dd class="mono">{granteeText(scope)}</dd>
    <dt>Created</dt>
    <dd>{new Date(scope.created_at).toLocaleString()}</dd>
    {#if scope.allow_download}
      <dt>Download</dt><dd>Allowed</dd>
    {/if}
    {#if scope.broker_status === "failed"}
      <dt>Attempts</dt><dd>{scope.broker_attempts}</dd>
      {#if scope.broker_last_error}
        <dt>Last error</dt><dd class="error">{scope.broker_last_error}</dd>
      {/if}
    {/if}
  </dl>

  {#if detailLoading}
    <div class="muted">Loading detail…</div>
  {/if}

  {#if scope.target_type === "album_live" && scope.target_album_id}
    <a
      href={`/albums/${scope.target_album_id}`}
      onclick={(e) => handleInternalLinkClick(e, `/albums/${scope.target_album_id}`)}
    >
      Open album →
    </a>
  {:else if scope.target_type === "media_set"}
    {#if !preview}
      <button type="button" onclick={loadPreview} disabled={previewLoading}>
        {previewLoading ? "Loading preview…" : "Preview"}
      </button>
    {:else}
      <div class="preview">
        <pre>{JSON.stringify(preview, null, 2)}</pre>
      </div>
    {/if}
  {/if}

  {#if detail?.media_ids && detail.media_ids.length > 0}
    <div class="muted">{detail.media_ids.length} photos</div>
  {/if}

  <div class="actions">
    {#if scope.broker_status === "failed"}
      <button type="button" onclick={() => onRetry(scope.uuid)}>Retry</button>
    {/if}
    {#if scope.broker_status !== "revoking" && scope.broker_status !== "revoked_remote"}
      <button type="button" class="danger" onclick={() => onRevoke(scope.uuid)}>Revoke</button>
    {/if}
  </div>
</aside>

<style>
  .drawer {
    position: fixed; right: 0; top: 0; bottom: 0; width: 360px;
    background: var(--bg-elevated); border-left: 1px solid var(--border);
    padding: 16px; overflow-y: auto; z-index: 50;
    display: flex; flex-direction: column; gap: 12px;
  }
  .drawer header { display: flex; align-items: center; gap: 8px; }
  .drawer h2 { margin: 0; font-size: 16px; flex: 1; }
  .close { background: transparent; border: 0; font-size: 20px; cursor: pointer; color: var(--text-muted); }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 4px 12px; font-size: 13px; margin: 0; }
  dt { color: var(--text-muted); }
  dd { margin: 0; }
  .mono { font-family: monospace; font-size: 12px; }
  .muted { color: var(--text-muted); font-size: 12px; }
  .error { color: var(--danger); white-space: pre-wrap; }
  .preview pre { background: var(--bg-surface); padding: 8px; font-size: 11px; overflow: auto; border-radius: 4px; margin: 0; }
  .actions { display: flex; gap: 8px; margin-top: auto; }
  .danger { color: var(--danger); border-color: var(--danger); }
</style>

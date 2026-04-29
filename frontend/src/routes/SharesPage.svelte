<script lang="ts">
  import SharesList from "../lib/components/SharesList.svelte";
  import ShareDrawer from "../lib/components/ShareDrawer.svelte";
  import ConfirmModal from "../lib/components/ConfirmModal.svelte";
  import type { SharesStore } from "../lib/shares/sharesStore.svelte";
  import { router } from "../lib/router/router.svelte";

  let { sharesStore }: { sharesStore: SharesStore } = $props();

  let openUuid = $state<string | null>(null);
  let revokingUuid = $state<string | null>(null);

  // Sync filter from route params on mount and on subsequent route changes.
  // The narrowing check is necessary because TS only narrows the union after
  // the discriminant check. void the async setter — filter changes are
  // user-perceived as instantaneous and the underlying refetch can resolve
  // in the background. Both filters mirror the URL symmetrically so
  // navigating from /shares?show_revoked=true to /shares clears the
  // toggle (URL is the source of truth on route change).
  $effect(() => {
    if (router.current.route !== "shares") return;
    const albumId = router.current.album_id ?? null;
    void sharesStore.setAlbumIDFilter(albumId);
    void sharesStore.setShowRevoked(router.current.show_revoked === true);
  });

  // Same quad-guard pattern as AlbumsIndex / AddToAlbumModal.
  // `scopes.length === 0` prevents re-runs after a successful first
  // fetch; `!exhausted` covers the truly-zero-shares case so the effect
  // doesn't loop indefinitely; `!loadError` breaks the auto-retry loop
  // when the API returns an error — the user must click Retry to clear
  // the flag (clicking the toggle also clears via setShowRevoked →
  // refetchListPreservingFilter).
  $effect(() => {
    if (
      sharesStore.scopes.length === 0 &&
      !sharesStore.loading &&
      !sharesStore.exhausted &&
      !sharesStore.loadError
    ) {
      sharesStore.loadInitial();
    }
  });

  function onOpen(uuid: string) {
    openUuid = uuid;
  }

  function onRevoke(uuid: string) {
    revokingUuid = uuid;
    openUuid = null;
  }

  async function confirmRevoke() {
    if (!revokingUuid) return;
    await sharesStore.revoke(revokingUuid);
    revokingUuid = null;
  }

  async function onRetry(uuid: string) {
    await sharesStore.retry(uuid);
  }

  // Mirror checkbox + filter changes back into the URL via
  // history.replaceState so refresh / back-forward preserves the user's
  // current filters. replaceState doesn't fire popstate, so the
  // route-sync $effect above won't re-run and clobber the user's
  // change — URL and store stay in sync without a feedback loop.
  function syncShowRevokedToUrl(next: boolean) {
    const url = new URL(window.location.href);
    if (next) url.searchParams.set("show_revoked", "true");
    else url.searchParams.delete("show_revoked");
    history.replaceState({}, "", url.toString());
  }
  function syncAlbumIDFilterToUrl(next: string | null) {
    const url = new URL(window.location.href);
    if (next) url.searchParams.set("album_id", next);
    else url.searchParams.delete("album_id");
    history.replaceState({}, "", url.toString());
  }

  // The confirm modal needs the grantee to render its title. Find the
  // matching scope from the current list — the user must have just seen
  // it in the table for the Revoke button to fire, so it'll be present.
  // ?? null narrows the find() result so the granteeName branch can
  // safely no-op when the row vanished mid-confirm (poll merged a remote
  // revoke).
  const revokingScope = $derived(
    revokingUuid ? sharesStore.scopes.find((s) => s.uuid === revokingUuid) ?? null : null,
  );
  const granteeName = $derived(
    revokingScope
      ? (revokingScope.grantee_handle ?? `${revokingScope.grantee.hub}:${revokingScope.grantee.user_id}`)
      : "",
  );

  // openScope re-derives whenever the scopes list refreshes (e.g. after
  // poll or retry merges new state), so the drawer reflects the latest
  // broker_status without a manual refresh.
  const openScope = $derived(
    openUuid ? sharesStore.scopes.find((s) => s.uuid === openUuid) ?? null : null,
  );
</script>

<header class="page-header">
  <h1>Shares</h1>
  <label class="show-revoked">
    <input
      type="checkbox"
      checked={sharesStore.showRevoked}
      onchange={(e) => {
        const next = (e.currentTarget as HTMLInputElement).checked;
        void sharesStore.setShowRevoked(next);
        syncShowRevokedToUrl(next);
      }}
    />
    <span>Show revoked</span>
  </label>
</header>

{#if sharesStore.albumIDFilter}
  <div class="filter-banner">
    <span>Showing shares for album <code>{sharesStore.albumIDFilter}</code></span>
    <button type="button" onclick={() => {
      void sharesStore.setAlbumIDFilter(null);
      syncAlbumIDFilterToUrl(null);
    }}>Clear filter</button>
  </div>
{/if}

{#if sharesStore.scopes.length > 0}
  <SharesList scopes={sharesStore.scopes} {onOpen} {onRevoke} {onRetry} />
{:else if sharesStore.loadError}
  <div class="empty">
    <p>Couldn't load shares.</p>
    <button type="button" onclick={() => sharesStore.retryLoad()}>Retry</button>
  </div>
{:else if !sharesStore.loading}
  <div class="empty">No shares yet. Share photos or albums from the Library or an album page.</div>
{/if}

{#if sharesStore.loading}<div class="loading">Loading…</div>{/if}

{#if openScope}
  <ShareDrawer
    scope={openScope}
    {sharesStore}
    onClose={() => (openUuid = null)}
    {onRevoke}
    {onRetry}
  />
{/if}

{#if revokingUuid}
  <ConfirmModal
    title={`Revoke share to ${granteeName}?`}
    body="Photos will become inaccessible to them."
    confirmLabel="Revoke"
    danger
    onConfirm={confirmRevoke}
    onCancel={() => (revokingUuid = null)}
  />
{/if}

<style>
  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .page-header h1 { margin: 0; font-size: 18px; }
  .show-revoked {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 13px;
    color: var(--text-muted);
  }
  .filter-banner {
    background: var(--bg-elevated);
    padding: 8px 16px;
    display: flex;
    gap: 12px;
    align-items: center;
    justify-content: space-between;
    font-size: 13px;
    border-bottom: 1px solid var(--border);
  }
  .filter-banner code { font-family: monospace; }
  .empty {
    padding: 64px 16px;
    text-align: center;
    color: var(--text-secondary);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 12px;
  }
  .loading { padding: 12px; color: var(--text-muted); }
</style>

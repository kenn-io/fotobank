<script lang="ts">
  import SharesList from "../lib/components/SharesList.svelte";
  import ConfirmModal from "../lib/components/ConfirmModal.svelte";
  import type { SharesStore } from "../lib/shares/sharesStore.svelte";

  let { sharesStore }: { sharesStore: SharesStore } = $props();

  let revokingUuid = $state<string | null>(null);

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
    // Drawer wiring lands in Task 22.
    console.log("open share", uuid);
  }

  function onRevoke(uuid: string) {
    revokingUuid = uuid;
  }

  async function confirmRevoke() {
    if (!revokingUuid) return;
    await sharesStore.revoke(revokingUuid);
    revokingUuid = null;
  }

  async function onRetry(uuid: string) {
    await sharesStore.retry(uuid);
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
</script>

<header class="page-header">
  <h1>Shares</h1>
  <label class="show-revoked">
    <input
      type="checkbox"
      checked={sharesStore.showRevoked}
      onchange={(e) => sharesStore.setShowRevoked((e.currentTarget as HTMLInputElement).checked)}
    />
    <span>Show revoked</span>
  </label>
</header>

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

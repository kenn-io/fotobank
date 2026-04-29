<script lang="ts">
  import AlbumGrid from "../lib/components/AlbumGrid.svelte";
  import NewAlbumForm from "../lib/components/NewAlbumForm.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";

  let { albumsStore }: { albumsStore: AlbumsStore } = $props();

  let modalOpen = $state(false);

  // The mount effect needs three guards. `albums.length === 0` ensures
  // we don't re-run loadInitial after a successful first fetch (without
  // it, a second response with `next_offset` set leaves `exhausted=false`
  // and the effect retriggers when loading flips back to false).
  // `!exhausted` ensures an account with truly zero albums doesn't loop
  // (length stays 0; exhausted=true after the first response, gating
  // the effect). `!loadError` breaks the auto-retry loop when the API
  // returns an error — the user must click Retry to clear the flag.
  $effect(() => {
    if (
      albumsStore.albums.length === 0 &&
      !albumsStore.loading &&
      !albumsStore.exhausted &&
      !albumsStore.loadError
    ) {
      albumsStore.loadInitial();
    }
  });

  // On every mount, check if the albums list was marked stale by a
  // hide/unhide in another route. If so, refetch page 1 to update counts
  // and covers. This is intentionally deferred to mount — we don't want
  // an eager refetch from Library or HiddenLibrary polluting the network
  // if the user never navigates to /albums.
  $effect(() => {
    void albumsStore.refreshIfStale();
  });

  let sentinel: HTMLDivElement | null = $state(null);
  $effect(() => {
    if (!sentinel) return;
    const io = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) albumsStore.loadMore();
    }, { rootMargin: "200px 0px" });
    io.observe(sentinel);
    return () => io.disconnect();
  });

  async function onCreate(name: string) {
    await albumsStore.create(name);
    modalOpen = false;
  }
</script>

<header class="page-header">
  <h1>Albums</h1>
  <button type="button" onclick={() => (modalOpen = true)}>+ New Album</button>
</header>

{#if albumsStore.albums.length > 0}
  <AlbumGrid albums={albumsStore.albums} />
{:else if albumsStore.loadError}
  <div class="empty">
    <p>Couldn't load albums.</p>
    <button type="button" onclick={() => albumsStore.retry()}>Retry</button>
  </div>
{:else if !albumsStore.loading}
  <div class="empty">
    <p>No albums yet</p>
    <button type="button" onclick={() => (modalOpen = true)}>Create your first album</button>
  </div>
{/if}

{#if albumsStore.loading}<div class="loading">Loading…</div>{/if}
<div bind:this={sentinel} style="height:1px"></div>

<svelte:window
  onkeydown={(e) => {
    if (modalOpen && e.key === "Escape") modalOpen = false;
  }}
/>

{#if modalOpen}
  <!-- Backdrop click dismisses; Esc dismisses via the window keydown
       handler above. Inner modal stops click propagation so interactions
       inside don't bubble up to the backdrop. -->
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal-backdrop" role="presentation" onclick={() => (modalOpen = false)}>
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="modal" role="dialog" aria-modal="true" aria-label="New album" tabindex="-1" onclick={(e) => e.stopPropagation()}>
      <h2>New album</h2>
      <NewAlbumForm {onCreate} onCancel={() => (modalOpen = false)} />
    </div>
  </div>
{/if}

<style>
  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .page-header h1 { font-size: 18px; margin: 0; }
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
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,0.5);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 16px;
    min-width: 320px;
  }
  .modal h2 { margin-top: 0; }
</style>

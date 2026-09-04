<script lang="ts">
  import { Button, EmptyState, Modal } from "@kenn-io/kit-ui";
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
  <Button tone="info" surface="solid" onclick={() => (modalOpen = true)}>New album</Button>
</header>

{#if albumsStore.albums.length > 0}
  <AlbumGrid albums={albumsStore.albums} />
{:else if albumsStore.loadError}
  <EmptyState title="Couldn't load albums">
    <Button onclick={() => albumsStore.retry()}>Retry</Button>
  </EmptyState>
{:else if !albumsStore.loading}
  <EmptyState
    title="No albums yet"
    description="Collect photos into albums for projects, trips, and people."
  >
    <Button tone="info" surface="solid" onclick={() => (modalOpen = true)}>
      Create your first album
    </Button>
  </EmptyState>
{/if}

{#if albumsStore.loading}<div class="loading">Loading…</div>{/if}
<div bind:this={sentinel} style="height:1px"></div>

{#if modalOpen}
  <Modal title="New album" width="360px" onclose={() => (modalOpen = false)}>
    <NewAlbumForm {onCreate} onCancel={() => (modalOpen = false)} />
  </Modal>
{/if}

<style>
  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border-default);
  }
  .page-header h1 { font-size: 18px; margin: 0; }
  .loading { padding: 12px; color: var(--text-muted); }
</style>

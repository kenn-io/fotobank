<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { AlbumsStore, AlbumListItem } from "../albums/albumsStore.svelte";
  import { normalizeForSearch } from "../format/normalizeForSearch";
  import { modalStack } from "../lightbox/modalStack.svelte";
  import NewAlbumForm from "./NewAlbumForm.svelte";

  let {
    mediaIds,
    albumsStore,
    onAdd,
    onClose,
  }: {
    mediaIds: string[];
    albumsStore: AlbumsStore;
    onAdd: (albumId: string) => Promise<{ added: number; already_present: number }>;
    onClose: () => void;
  } = $props();

  let mode = $state<"list" | "create">("list");
  let query = $state("");
  let selectedId = $state<string | null>(null);
  let pending = $state(false);
  let error = $state<string | null>(null);

  const modalId = `add-to-album-${Math.random().toString(36).slice(2)}`;
  // onEscape returns false while pending so a later Esc can still
  // close the modal once the in-flight save settles.
  onMount(() => modalStack.push({
    id: modalId,
    onEscape: () => {
      if (pending) return false;
      onClose();
    },
  }));
  onDestroy(() => modalStack.pop(modalId));

  // Users may open Add-to-album before ever visiting /albums, so the
  // store may not yet be hydrated. Same guard pattern as AlbumsIndex:
  // empty-list AND not-loading AND not-exhausted AND not-loadError is
  // the only state that warrants an auto loadInitial — without the
  // loadError gate, a transient 5xx would loop the effect.
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

  const subtitle = $derived(`${mediaIds.length} ${mediaIds.length === 1 ? "photo" : "photos"}`);
  const primaryLabel = $derived(`Add ${mediaIds.length} ${mediaIds.length === 1 ? "photo" : "photos"}`);

  // $derived.by(...) — multi-statement derived. $derived(() => ...) would
  // yield a function-typed value (see AlbumDetail.svelte:23 precedent).
  const filtered: AlbumListItem[] = $derived.by((): AlbumListItem[] => {
    const q = normalizeForSearch(query);
    if (q === "") return albumsStore.albums;
    return albumsStore.albums.filter((a) => normalizeForSearch(a.name).includes(q));
  });

  async function onCreateNew(name: string) {
    // NewAlbumForm catches and surfaces errors from onCreate. Don't add a
    // try/catch here — let creation errors propagate to NewAlbumForm.
    // AlbumsStore.create returns the created album's id, which is the
    // only reliable way to identify it: a name-match would target the
    // wrong row when albums share a name, and the post-create refetch
    // may not yet have placed the row in the list when this returns.
    const newId = await albumsStore.create(name);
    selectedId = newId;
    mode = "list";
  }

  async function submit() {
    if (!selectedId || pending) return;
    pending = true;
    error = null;
    try {
      await onAdd(selectedId);
      onClose();
    } catch (err: unknown) {
      // huma errors surface as {title, detail}; inline thrown JS Errors as {message}.
      const e = (err ?? {}) as { message?: string; detail?: string; title?: string };
      error = e.detail ?? e.message ?? e.title ?? "Failed to add photos";
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
  <div class="modal" role="dialog" aria-modal="true" aria-label="Add to album" tabindex="-1" onclick={(e) => e.stopPropagation()}>
    <h2>Add to album</h2>
    <div class="subtitle">{subtitle}</div>

    {#if mode === "list"}
      <!-- svelte-ignore a11y_autofocus -->
      <input
        type="text"
        class="search"
        placeholder="Search albums..."
        bind:value={query}
        autofocus
      />
      <div class="list">
        <button type="button" class="row create-new" onclick={() => (mode = "create")}>
          + Create new album
        </button>
        {#each filtered as a (a.id)}
          <button
            type="button"
            class="row"
            class:selected={selectedId === a.id}
            onclick={() => (selectedId = a.id)}
          >
            <span class="name">{a.name}</span>
            <span class="count">{a.item_count}</span>
          </button>
        {/each}
        {#if albumsStore.loadError}
          <div class="row error-row" role="alert">
            <span class="error-msg">Failed to load albums.</span>
            <button type="button" onclick={() => albumsStore.retry()}>Retry</button>
          </div>
        {:else if !albumsStore.exhausted}
          <button
            type="button"
            class="row load-more"
            onclick={() => albumsStore.loadMore()}
            disabled={albumsStore.loading}
          >
            {albumsStore.loading ? "Loading…" : "Load more albums"}
          </button>
        {/if}
      </div>
      {#if error}<div class="error" role="alert">{error}</div>{/if}
      <div class="actions">
        <button type="button" onclick={onClose} disabled={pending}>Cancel</button>
        <button type="button" onclick={submit} disabled={!selectedId || pending}>
          {pending ? "Adding…" : primaryLabel}
        </button>
      </div>
    {:else}
      <NewAlbumForm onCreate={onCreateNew} onCancel={() => (mode = "list")} />
    {/if}
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed; inset: 0;
    background: var(--bg-overlay);
    display: flex; align-items: center; justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-md);
    padding: var(--space-6);
    min-width: 400px;
    max-height: 80vh;
    display: flex; flex-direction: column;
    box-shadow: var(--shadow-md);
  }
  .modal h2 { margin-top: 0; }
  .subtitle {
    color: var(--text-muted);
    font-size: var(--text-base);
    margin-bottom: var(--space-5);
  }
  .search {
    padding: var(--space-3) var(--space-4);
    border: 1px solid var(--border);
    background: var(--bg-surface);
    color: var(--text-primary);
    border-radius: var(--radius-sm);
  }
  .list {
    margin-top: var(--space-5);
    overflow-y: auto;
    flex: 1;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .row {
    display: flex;
    justify-content: space-between;
    width: 100%;
    padding: var(--space-4) var(--space-5);
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border);
    color: var(--text-primary);
    cursor: pointer;
    text-align: left;
  }
  .row:last-child { border-bottom: 0; }
  .row:hover { background: var(--bg-hover); }
  .row.selected {
    background: var(--bg-hover);
    font-weight: 600;
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }
  .row.create-new { color: var(--accent); font-weight: 500; }
  .row.load-more { color: var(--text-muted); font-style: italic; justify-content: center; }
  .row.load-more:disabled { cursor: not-allowed; opacity: 0.6; }
  .row.error-row { color: var(--danger); align-items: center; }
  .error-msg { flex: 1; }
  .count { color: var(--text-muted); font-size: var(--text-sm); }
  .error {
    color: var(--danger);
    font-size: var(--text-base);
    margin-top: var(--space-4);
  }
  .actions {
    display: flex; gap: var(--space-4); justify-content: flex-end;
    margin-top: var(--space-5);
  }
</style>

<!-- frontend/src/routes/HiddenLibrary.svelte
     Route component for /hidden. Wraps HiddenGate and shows a grid of
     hidden media when unlocked. HiddenMediaStore is route-scoped:
     created here and torn down when this component unmounts.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import HiddenGate from "../lib/components/HiddenGate.svelte";
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import ActionBar from "../lib/components/ActionBar.svelte";
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import { selection } from "../lib/selection/selectionStore.svelte";
  import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";
  import { ScrollRestore, captureMainScrollY } from "../lib/lightbox/scrollRestore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
  import { HiddenMediaStore } from "../lib/hidden/hiddenMediaStore.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import type { AppConfigStore } from "../lib/app/appConfig.svelte";
  import { router } from "../lib/router/router.svelte";
  import { api } from "../lib/api/client";

  let {
    hiddenStore,
    albumsStore,
    toastStore,
    appConfig,
    // Optional override for testing. When omitted, the route constructs
    // its own route-scoped store backed by the live api client.
    hiddenMediaStore = new HiddenMediaStore(api),
  }: {
    hiddenStore: HiddenStore;
    albumsStore: AlbumsStore;
    toastStore: ToastStore;
    appConfig: AppConfigStore;
    hiddenMediaStore?: HiddenMediaStore;
  } = $props();

  // Load initial items once when the store becomes configured+unlocked.
  // Use untrack so the effect doesn't subscribe to hiddenMediaStore's
  // pagination state (loading, exhausted, months), which would cause an
  // infinite refetch loop (finding #5). A `started` guard ensures we
  // only call loadInitial once per mount even if hiddenStore re-renders.
  let started = false;
  $effect(() => {
    if (hiddenStore.configured && hiddenStore.unlocked && !started) {
      started = true;
      untrack(() => void hiddenMediaStore.loadInitial());
    }
  });

  // Watch for 403 from the hidden media store — the session expired
  // mid-view. Clear the unlock state so the gate re-appears (finding #6),
  // and reset the route-scoped store + the `started` guard so the next
  // unlock on this same mount triggers a fresh loadInitial(). Without
  // the resets, loadError=403 + started=true would leave the grid empty
  // and silent until the user navigated away.
  $effect(() => {
    if (hiddenMediaStore.loadError === 403) {
      void hiddenStore.lock();
      hiddenMediaStore.reset();
      started = false;
    }
  });

  // Scroll restoration mirrors Library/Sessions: when this route remounts
  // after a lightbox close on a hidden snapshot, consume scrollY/focus
  // and clear them, then attempt restore on each months change so
  // paginated reloads can land the saved Y once enough content exists.
  const restore = new ScrollRestore();

  $effect(() => {
    const snap = lightboxSession.snapshot;
    if (snap === null || snap.source.kind !== "hidden") return;
    // Skip when there's nothing to restore — same guard as Library.svelte.
    if (snap.scrollY === 0 && snap.returnFocusMediaId === null) return;
    restore.markPending({ scrollY: snap.scrollY, mediaId: snap.returnFocusMediaId });
    lightboxSession.clearScroll();
    lightboxSession.clearReturnFocus();
  });
  $effect(() => {
    void hiddenMediaStore.months;
    if (restore.isPending()) restore.attemptRestore();
  });

  // openMedia captures the hidden source state into lightboxSession
  // before navigating to /media/:id?from=hidden. The flat id list comes
  // from hiddenMediaStore.months — months are sorted DESC and items
  // within each month are sorted DESC by taken time, so the resulting
  // navIds order matches what the user sees on screen. Selection
  // narrowing matches Library/Sessions/AlbumDetail.
  function openMedia(mid: string) {
    const all: string[] = [];
    for (const m of hiddenMediaStore.months) {
      for (const it of m.items) all.push(it.id);
    }
    const sel = selection.ids;
    const useSelection = sel.size > 1 && sel.has(mid);
    const navIds = useSelection ? all.filter((x) => sel.has(x)) : all;
    lightboxSession.open({
      source: { kind: "hidden" },
      navIds,
      selected: useSelection,
      scrollY: captureMainScrollY(),
      returnFocusMediaId: mid,
      returnHref: "/hidden",
    });
    router.navigate(`/media/${mid}?from=hidden`);
  }

  // Filter the global selection to IDs that are actually present in the
  // hidden store — the global selection is shared across routes and may
  // contain IDs from Library/Sessions (finding #9).
  const selectedInHidden = $derived.by((): string[] => {
    void hiddenMediaStore.months; // register dep
    const hiddenIds = new Set(
      hiddenMediaStore.months.flatMap((m) => m.items.map((it) => it.id)),
    );
    return Array.from(selection.ids).filter((id) => hiddenIds.has(id));
  });

  let addOpen = $state(false);
  let pendingIds = $state<string[]>([]);
  function openAdd(ids: string[]) { pendingIds = ids; addOpen = true; }

  async function onAdd(albumId: string): Promise<{ added: number; already_present: number }> {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingIds } as never,
    });
    if (res.error) throw res.error;
    selection.clear();
    return res.data as { added: number; already_present: number };
  }

  async function onUnhide(ids: string[]): Promise<void> {
    if (!window.confirm(`Unhide ${ids.length} photo${ids.length === 1 ? "" : "s"}?`)) return;
    let result;
    try {
      result = await hiddenStore.unhide(ids);
    } catch {
      toastStore.push({ message: "Unhide failed. Try again.", kind: "error" });
      return;
    }
    const succeeded = result.succeeded ?? [];
    if (succeeded.length > 0) {
      hiddenMediaStore.removeMany(succeeded);
      selection.removeAll(succeeded);
      // Mark albums stale so counts refresh on next visit.
      albumsStore.markStale();
    }
    const failed = result.failed ?? [];
    if (failed.length > 0) {
      toastStore.push({
        message: `${failed.length} photo${failed.length === 1 ? "" : "s"} could not be unhidden.`,
        details: failed.map((f) => `${f.id}: ${f.code}`),
        kind: "error",
      });
    }
  }
</script>

<HiddenGate {hiddenStore}>
  {#snippet children()}
    <ActionBar {selection} selectedCount={selectedInHidden.length}>
      {#snippet actions()}
        <MediaActions
          mediaIds={selectedInHidden}
          context="hidden"
          {appConfig}
          onAdd={openAdd}
          onShare={() => {}}
          {onUnhide}
        />
      {/snippet}
    </ActionBar>

    <VirtualGrid
      months={hiddenMediaStore.months}
      onLoadMore={() => hiddenMediaStore.loadMore()}
      onOpenMedia={openMedia}
    />

    {#if hiddenMediaStore.loading}
      <div style="padding:12px; color: var(--text-muted)">Loading…</div>
    {/if}
    {#if hiddenMediaStore.months.length === 0 && !hiddenMediaStore.loading}
      <div style="padding:24px; color: var(--text-secondary)">No hidden photos.</div>
    {/if}
  {/snippet}
</HiddenGate>

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}

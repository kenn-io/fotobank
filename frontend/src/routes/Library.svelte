<!-- frontend/src/routes/Library.svelte -->
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import GroupSelectButton from "../lib/components/GroupSelectButton.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import ActionBar from "../lib/components/ActionBar.svelte";
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import ShareModal from "../lib/components/ShareModal.svelte";
  import { selection } from "../lib/selection/selectionStore.svelte";
  import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";
  import { ScrollRestore, captureMainScrollY } from "../lib/lightbox/scrollRestore.svelte";
  import { flattenLibraryIds } from "../lib/lightbox/sessionsFlatten";
  import { router } from "../lib/router/router.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import type { AppConfigStore } from "../lib/app/appConfig.svelte";
  import type { CreateShareBody } from "../lib/share/shareTypes";
  import { api } from "../lib/api/client";
  import FilterChipStrip from "../lib/filters/FilterChipStrip.svelte";
  import { isEmpty, type ActiveFilters } from "../lib/filters/activeFilters";

  let {
    mediaStore,
    albumsStore,
    hiddenStore,
    toastStore,
    appConfig,
    activeFilters,
    tagLabels,
    onFiltersChange,
  }: {
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
    appConfig: AppConfigStore;
    activeFilters: ActiveFilters;
    tagLabels: Record<string, string>;
    onFiltersChange: (next: ActiveFilters) => void;
  } = $props();

  // Drive the mediaStore on every filter change. setFilters is a no-op
  // when the filterKey is unchanged (router replays rebuild
  // ActiveFilters from URL params on every navigation), so this only
  // triggers a real reload when the user actually toggled a chip.
  // loadInitial walks loadMore which is re-entrant — a stale fetch in
  // flight is invalidated by setFilters bumping fetchToken.
  $effect(() => {
    mediaStore.setFilters(activeFilters);
    void mediaStore.loadInitial();
  });

  const density = new DensityStore(api, "library");
  density.load();

  // Scroll restoration: when this route remounts after a lightbox close,
  // pull scrollY + focus media id off the snapshot, mark restore pending,
  // and clear the snapshot so the next remount doesn't re-trigger. The
  // restore retries on every months change until either the target cell
  // exists or scrollHeight covers the saved Y (see ScrollRestore).
  const restore = new ScrollRestore();

  $effect(() => {
    const snap = lightboxSession.snapshot;
    if (snap === null || snap.source.kind !== "library") return;
    // Skip when there's nothing to restore. clearScroll/clearReturnFocus
    // re-trigger this effect by reassigning the snapshot; without this
    // guard the second run would overwrite the pending restore data with
    // scrollY=0/mediaId=null and the actual position would be lost.
    if (snap.scrollY === 0 && snap.returnFocusMediaId === null) return;
    restore.markPending({ scrollY: snap.scrollY, mediaId: snap.returnFocusMediaId });
    lightboxSession.clearScroll();
    lightboxSession.clearReturnFocus();
  });
  $effect(() => {
    void mediaStore.months;
    if (restore.isPending()) restore.attemptRestore();
  });

  // openMedia captures the current source state into lightboxSession
  // before navigating to /media/:id?from=library. If the user has a
  // multi-selection that includes the clicked id, narrow navIds to the
  // selection so prev/next walks only the selected set; otherwise walk
  // the full flattened library. The scroll container is `.main`
  // (overflow:auto in ThreeColumnLayout); captureMainScrollY reads from
  // there so ScrollRestore (which targets `.main` by default) can
  // restore the same Y on remount.
  function openMedia(id: string) {
    const all = flattenLibraryIds(mediaStore.months);
    const sel = selection.ids;
    const useSelection = sel.size > 1 && sel.has(id);
    const navIds = useSelection ? all.filter((x) => sel.has(x)) : all;
    lightboxSession.open({
      source: { kind: "library" },
      navIds,
      selected: useSelection,
      scrollY: captureMainScrollY(),
      returnFocusMediaId: id,
      returnHref: "/library",
    });
    router.navigate(`/media/${id}?from=library`);
  }

  let addOpen = $state(false);
  let shareOpen = $state(false);
  let pendingIds = $state<string[]>([]);

  function openAdd(ids: string[]) { pendingIds = ids; addOpen = true; }
  function openShare(ids: string[]) { pendingIds = ids; shareOpen = true; }

  async function onAdd(albumId: string): Promise<{ added: number; already_present: number }> {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingIds } as never,
    });
    if (res.error) throw res.error;
    selection.clear();
    return res.data as { added: number; already_present: number };
  }

  async function onCreateShare(body: CreateShareBody): Promise<void> {
    const res = await api.POST("/api/v1/shares", { body: body as never });
    if (res.error) throw res.error;
    // ShareModal calls onClose() itself on success — match the
    // AddToAlbumModal contract; no need to flip shareOpen here.
    selection.clear();
  }

  async function onHide(ids: string[]): Promise<void> {
    if (!window.confirm(`Hide ${ids.length} photo${ids.length === 1 ? "" : "s"}?`)) return;
    let result;
    try {
      result = await hiddenStore.hide(ids);
    } catch {
      toastStore.push({ message: "Hide failed. Try again.", kind: "error" });
      return;
    }
    const succeeded = result.succeeded ?? [];
    if (succeeded.length > 0) {
      mediaStore.removeMany(succeeded);
      selection.removeAll(succeeded);
      // Mark albums stale so cover and counts refresh on next visit.
      albumsStore.markStale();
    }
    const failed = result.failed ?? [];
    if (failed.length > 0) {
      toastStore.push({
        message: `${failed.length} photo${failed.length === 1 ? "" : "s"} could not be hidden.`,
        details: failed.map((f) => `${f.id}: ${f.code}`),
        kind: "error",
      });
    }
  }
</script>

<ActionBar {selection}>
  {#snippet actions()}
    <MediaActions
      mediaIds={Array.from(selection.ids)}
      context="library"
      hiddenConfigured={hiddenStore.configured}
      {appConfig}
      onAdd={openAdd}
      onShare={openShare}
      {onHide}
    />
  {/snippet}
</ActionBar>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>

<FilterChipStrip filters={activeFilters} {tagLabels} onChange={onFiltersChange} />

<VirtualGrid
  months={mediaStore.months}
  onLoadMore={() => mediaStore.loadMore()}
  targetRowHeight={density.targetRowHeight}
  onOpenMedia={openMedia}
>
  {#snippet headerAction(month)}
    <GroupSelectButton
      ids={month.items.map((it) => it.id)}
      label={month.key}
    />
  {/snippet}
</VirtualGrid>

{#if mediaStore.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if mediaStore.months.length === 0 && !mediaStore.loading}
  {#if !isEmpty(activeFilters)}
    <div class="empty-filtered" role="status">
      <p>No photos match these filters.</p>
      <button
        type="button"
        class="clear-all"
        onclick={() =>
          onFiltersChange({
            cameras: [],
            lenses: [],
            tagKeys: [],
            hasGps: null,
            mediaType: null,
          })}
      >
        Clear all
      </button>
    </div>
  {:else}
    <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
  {/if}
{/if}

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}

{#if appConfig.sharingEnabled && shareOpen}
  <ShareModal
    target={{ type: "media_set", mediaIds: pendingIds }}
    onCreate={onCreateShare}
    onClose={() => (shareOpen = false)}
  />
{/if}

<style>
  .empty-filtered {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-3);
    padding: 48px var(--space-4);
    color: var(--text-secondary);
    text-align: center;
  }
  .empty-filtered p {
    margin: 0;
    font-size: var(--font-size-sm);
  }
  .empty-filtered .clear-all {
    display: inline-flex;
    align-items: center;
    height: 28px;
    padding: 0 12px;
    background: color-mix(in srgb, var(--accent-blue) 14%, transparent);
    color: var(--accent-blue);
    border: 1px solid color-mix(in srgb, var(--accent-blue) 24%, transparent);
    font-size: var(--font-size-xs);
    font-weight: 500;
    cursor: pointer;
    transition: background 100ms;
  }
  .empty-filtered .clear-all:hover {
    background: color-mix(in srgb, var(--accent-blue) 22%, transparent);
  }
</style>

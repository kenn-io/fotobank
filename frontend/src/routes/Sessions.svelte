<!-- frontend/src/routes/Sessions.svelte -->
<script lang="ts">
  import MonthChunk from "../lib/grid/MonthChunk.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import { groupIntoSessions } from "../lib/sessions/sessionGrouping";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import { api } from "../lib/api/client";
  import MediaCell from "../lib/grid/MediaCell.svelte";
  import { router } from "../lib/router/router.svelte";
  import { selection } from "../lib/selection/selectionStore.svelte";
  import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";
  import { ScrollRestore, captureMainScrollY } from "../lib/lightbox/scrollRestore.svelte";
  import { flattenSessionIds } from "../lib/lightbox/sessionsFlatten";
  import GroupSelectButton from "../lib/components/GroupSelectButton.svelte";
  import ActionBar from "../lib/components/ActionBar.svelte";
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import ShareModal from "../lib/components/ShareModal.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import type { CreateShareBody } from "../lib/share/shareTypes";

  let {
    mediaStore,
    albumsStore,
    hiddenStore,
    toastStore,
  }: {
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
  } = $props();

  const density = new DensityStore(api, "sessions");
  density.load();
  const flat = $derived(mediaStore.months.flatMap((m) => m.items));
  const sessions = $derived(groupIntoSessions(flat, { gapHours: 4 }));
  const orderedIds = $derived(
    sessions.flatMap((s) => s.items.map((it) => it.id)),
  );

  // Scroll restoration mirrors Library.svelte: on remount after a
  // lightbox close, consume the snapshot's scrollY/focus and clear
  // them, then attempt restore on each months change.
  const restore = new ScrollRestore();

  $effect(() => {
    const snap = lightboxSession.snapshot;
    if (snap !== null && snap.source.kind === "sessions") {
      restore.markPending({ scrollY: snap.scrollY, mediaId: snap.returnFocusMediaId });
      lightboxSession.clearScroll();
      lightboxSession.clearReturnFocus();
    }
  });
  $effect(() => {
    void mediaStore.months;
    if (restore.isPending()) restore.attemptRestore();
  });

  // openMedia captures source state into lightboxSession before
  // navigating to /media/:id?from=sessions. If a multi-selection covers
  // the clicked id, navIds narrows to it; otherwise it walks the full
  // session-flattened list. captureMainScrollY reads from `.main` (the
  // overflow:auto scroller in ThreeColumnLayout) so ScrollRestore can
  // restore the same Y on remount.
  function openMedia(id: string) {
    const all = flattenSessionIds(mediaStore.months);
    const sel = selection.ids;
    const useSelection = sel.size > 1 && sel.has(id);
    const navIds = useSelection ? all.filter((x) => sel.has(x)) : all;
    lightboxSession.open({
      source: { kind: "sessions" },
      navIds,
      selected: useSelection,
      scrollY: captureMainScrollY(),
      returnFocusMediaId: id,
      returnHref: "/sessions",
    });
    router.navigate(`/media/${id}?from=sessions`);
  }

  // Click policy mirrors VirtualGrid.handleCellClick so /library and
  // /sessions feel identical: button-0 only, modifier branches each
  // preventDefault before mutating selection, plain click opens the
  // lightbox. If you change one site, change the other (or extract a
  // shared helper once a third site lands).
  function handleCellClick(e: MouseEvent, id: string) {
    if (e.button !== 0) return;
    if (e.shiftKey) {
      e.preventDefault();
      selection.range(id, orderedIds);
      return;
    }
    if (e.metaKey || e.ctrlKey) {
      e.preventDefault();
      selection.toggle(id);
      return;
    }
    e.preventDefault();
    openMedia(id);
  }

  let containerEl: HTMLDivElement | null = $state(null);
  let containerWidth = $state(800);
  let sentinel: HTMLDivElement | null = $state(null);

  $effect(() => {
    if (!containerEl) return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width;
      if (w) containerWidth = w;
    });
    ro.observe(containerEl);
    return () => ro.disconnect();
  });

  // Pull more pages when the user scrolls near the bottom — sessions
  // cluster across the full library, so capping at the first page hides
  // older trips. The 800px rootMargin matches VirtualGrid's so the next
  // page is in flight before the sentinel is on-screen.
  $effect(() => {
    if (!sentinel) return;
    const io = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) mediaStore.loadMore();
    }, { rootMargin: "800px 0px" });
    io.observe(sentinel);
    return () => io.disconnect();
  });

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
      context="session"
      hiddenConfigured={hiddenStore.configured}
      onAdd={openAdd}
      onShare={openShare}
      {onHide}
    />
  {/snippet}
</ActionBar>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>

<div bind:this={containerEl} style="padding: 8px;">
  {#each sessions as s (s.id)}
    {@const first = s.items[0]}
    {#if first}
      <MonthChunk
        items={s.items.map((m) => ({ id: m.id, aspect: m.aspect, thumbUrl: m.thumbUrl }))}
        label={`${first.taken.toUTCString().slice(0, 16)} · ${s.items.length} photos`}
        options={{ containerWidth, targetRowHeight: density.targetRowHeight, gap: 4 }}
      >
        {#snippet renderCell(m)}
          <MediaCell
            media={m}
            selected={selection.ids.has(m.id)}
            onCellClick={(e) => handleCellClick(e, m.id)}
          />
        {/snippet}
        {#snippet headerAction()}
          <GroupSelectButton
            ids={s.items.map((m) => m.id)}
            label={`${first.taken.toUTCString().slice(0, 16)} session`}
          />
        {/snippet}
      </MonthChunk>
    {/if}
  {/each}
  <div bind:this={sentinel} style="height:1px"></div>
</div>

{#if mediaStore.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if mediaStore.months.length === 0 && !mediaStore.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}

{#if shareOpen}
  <ShareModal
    target={{ type: "media_set", mediaIds: pendingIds }}
    onCreate={onCreateShare}
    onClose={() => (shareOpen = false)}
  />
{/if}

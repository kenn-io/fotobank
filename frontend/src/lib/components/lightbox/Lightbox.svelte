<!-- frontend/src/lib/components/lightbox/Lightbox.svelte -->
<!--
  Top-level lightbox shell. Snapshot-driven happy path (T16):

  - Reads the open snapshot from lightboxSession; verifies the route
    `from` query param agrees with the snapshot's source.
  - Renders the active media's progressive image, prev/next buttons,
    toolbar, and (optionally) info drawer/sheet.
  - On session miss / hidden cross-context / fetch failure, falls back
    to a chrome-wrapped DirectMediaDetail (or a 404 message).
  - Reconstruction (the `idle → running → ok | failed` flow when no
    snapshot is present) is added in T17. For T16 reconstructionState
    is "ok" if the snapshot matches and "idle" otherwise — the
    fallback path covers the latter.

  Exposes nothing imperative. Modal stack registration is per-instance
  via a random `modalId` so multiple Lightboxes never clash.
-->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { router } from "../../router/router.svelte";
  import { isEditableTarget } from "../../dom/editable";
  import type { MediaStore, Media } from "../../media/mediaStore.svelte";
  import type { AlbumsStore } from "../../albums/albumsStore.svelte";
  import type { HiddenStore } from "../../hidden/hiddenStore.svelte";
  import type { ToastStore } from "../../toasts/toastStore.svelte";
  import { toMedia } from "../../media/mediaStore.svelte";
  import { lightboxSession } from "../../lightbox/lightboxSession.svelte";
  import type { LightboxSource } from "../../lightbox/lightboxSession.svelte";
  import { modalStack } from "../../lightbox/modalStack.svelte";
  import { computeNav } from "../../lightbox/lightboxNav.svelte";
  import { LightboxLoader, thumbUrl } from "../../lightbox/lightboxLoader";
  import LightboxFrame from "./LightboxFrame.svelte";
  import LightboxToolbar from "./LightboxToolbar.svelte";
  import LightboxNavButtons from "./LightboxNavButtons.svelte";
  import LightboxMedia from "./LightboxMedia.svelte";
  import LightboxActions from "./LightboxActions.svelte";
  import LightboxInfoDrawer from "./LightboxInfoDrawer.svelte";
  import LightboxInfoSheet from "./LightboxInfoSheet.svelte";
  import type { LightboxImageApi } from "./LightboxImage.svelte";
  import DirectMediaDetail from "../DirectMediaDetail.svelte";
  import AddToAlbumModal from "../AddToAlbumModal.svelte";
  import ShareModal from "../ShareModal.svelte";
  import { api } from "../../api/client";
  import type { CreateShareBody } from "../../share/shareTypes";

  let {
    id,
    from,
    mediaStore,
    albumsStore,
    hiddenStore,
    toastStore,
  }: {
    id: string;
    from: string;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
  } = $props();

  // ---- Modal stack registration ----------------------------------
  const modalId = `lightbox-${Math.random().toString(36).slice(2)}`;
  onMount(() => {
    modalStack.push({ id: modalId, onEscape: close });
    document.body.style.overflow = "hidden";
  });
  onDestroy(() => {
    modalStack.pop(modalId);
    document.body.style.overflow = "";
  });

  // ---- Source / session resolution -------------------------------
  // The snapshot is the source of truth for navIds and returnHref.
  // `from` (the route query param) must agree with the snapshot's
  // source — if not, we treat this as a reconstruction case (T17) and
  // for T16 fall through to the DirectMediaDetail fallback.
  const session = $derived(lightboxSession.snapshot);
  const fromMatchesSession = $derived.by(() => {
    if (session === null) return false;
    const src = session.source;
    switch (src.kind) {
      case "library":
        return from === "library";
      case "sessions":
        return from === "sessions";
      case "hidden":
        return from === "hidden";
      case "album":
        return from === `album:${src.albumId}`;
      default: {
        const _exhaustive: never = src;
        void _exhaustive;
        return false;
      }
    }
  });
  const navIds = $derived(
    fromMatchesSession && session !== null ? session.navIds : [],
  );
  const nav = $derived(computeNav(navIds, id));
  const returnHref = $derived(session?.returnHref ?? "/library");

  // ---- Media data ------------------------------------------------
  // Mirrors DirectMediaDetail's derive-from-store-with-fallback
  // pattern: read the visible store first, fall back to the raw fetch
  // payload (which may be a hidden row that the visible store rejects).
  let lastRaw = $state<Record<string, unknown> | null>(null);
  let loadError = $state<number | null>(null);
  const media = $derived.by((): Media | undefined => {
    void mediaStore.months;
    const m = mediaStore.get(id);
    if (m) return m;
    if (lastRaw) {
      const parsed = toMedia(lastRaw);
      return parsed ?? undefined;
    }
    return undefined;
  });
  const rawHidden = $derived(lastRaw?.["hidden_at"]);
  const isHidden = $derived(rawHidden !== undefined && rawHidden !== null);

  // ---- Fetch effect ----------------------------------------------
  // On id change: clear stale state, then fetch only when the row is
  // not already in the visible store. Cancellation flag prevents a
  // late response from clobbering a newer one.
  $effect(() => {
    const currentId = id;
    loadError = null;
    lastRaw = null;
    if (mediaStore.get(currentId)) return;
    let cancelled = false;
    (async () => {
      try {
        const resp = await fetch(`/api/v1/media/${currentId}`);
        if (cancelled) return;
        if (!resp.ok) {
          loadError = resp.status;
          return;
        }
        const raw = await resp.json();
        if (cancelled) return;
        lastRaw = raw as Record<string, unknown>;
        mediaStore.mergeRaw([raw]);
      } catch {
        if (cancelled) return;
        loadError = -1;
      }
    })();
    return () => {
      cancelled = true;
    };
  });

  // ---- Fallback decision -----------------------------------------
  // hiddenCrossContext: a hidden row was opened with a non-hidden
  // `from` — render via DirectMediaDetail so the unhide flow runs in
  // the standalone surface (Lightbox is library-shaped, not hidden).
  const hiddenCrossContext = $derived(isHidden && from !== "hidden");
  // Tri-state reconstruction. For T16 the value is "ok" when the
  // snapshot matches and "idle" otherwise — the latter falls into
  // fallback. T17 will widen this with "running" / "failed" for the
  // direct-entry / no-snapshot case.
  type RecState = "idle" | "running" | "ok" | "failed";
  const reconstructionState = $derived<RecState>(
    fromMatchesSession ? "ok" : "idle",
  );
  const reconstructionInFlight = $derived(
    !fromMatchesSession &&
      (reconstructionState === "idle" || reconstructionState === "running"),
  );
  const reconstructionFailed = $derived(
    !fromMatchesSession && reconstructionState === "failed",
  );
  const fallbackMode = $derived(
    loadError !== null || hiddenCrossContext || reconstructionFailed,
  );
  const notFoundMode = $derived(loadError !== null);

  // ---- Image loader ----------------------------------------------
  const loader = new LightboxLoader();
  let visibleSrc = $state("");
  $effect(() => {
    const m = media;
    if (!m || fallbackMode) return;
    const gridSrc = m.thumbUrl;
    const previewUrl = thumbUrl(m.id, "preview", m.thumbVersion);
    const largeUrl = thumbUrl(m.id, "large", m.thumbVersion);
    void loader.load({
      activeId: m.id,
      gridSrc,
      previewUrl,
      largeUrl,
      onSrc: (u) => (visibleSrc = u),
    });
    const prevId = nav.prevId;
    const nextId = nav.nextId;
    const prevMedia = prevId !== null ? mediaStore.get(prevId) : undefined;
    const nextMedia = nextId !== null ? mediaStore.get(nextId) : undefined;
    loader.prefetch({
      prevPreviewUrl: prevMedia
        ? thumbUrl(prevMedia.id, "preview", prevMedia.thumbVersion)
        : null,
      nextPreviewUrl: nextMedia
        ? thumbUrl(nextMedia.id, "preview", nextMedia.thumbVersion)
        : null,
      prevLargeUrl: prevMedia
        ? thumbUrl(prevMedia.id, "large", prevMedia.thumbVersion)
        : null,
      nextLargeUrl: nextMedia
        ? thumbUrl(nextMedia.id, "large", nextMedia.thumbVersion)
        : null,
    });
  });

  // ---- Navigation -----------------------------------------------
  function navTo(targetId: string) {
    router.navigate(`/media/${targetId}?from=${encodeURIComponent(from)}`, {
      replace: true,
    });
  }
  function close() {
    router.back(returnHref);
    lightboxSession.close();
  }
  function onPrev() {
    if (nav.prevId !== null) navTo(nav.prevId);
  }
  function onNext() {
    if (nav.nextId !== null) navTo(nav.nextId);
  }
  function onActionDone(_op: "hide" | "unhide", succeeded: string[]) {
    if (succeeded.length === 0) return;
    if (navIds.length === 0) {
      close();
      return;
    }
    if (!navIds.includes(id)) {
      const advanceTo = nav.nextId ?? nav.prevId;
      if (advanceTo !== null) navTo(advanceTo);
      else close();
    }
  }

  // ---- Image api (T13 LightboxImage onReady) --------------------
  let imageApi = $state<LightboxImageApi | null>(null);

  // ---- Keyboard shortcuts ---------------------------------------
  // Topmost-modal guard so an open AddToAlbum / Share modal swallows
  // these keys. Editable-target guard so typing into an input or
  // textarea doesn't navigate or zoom.
  function onKey(e: KeyboardEvent) {
    if (!modalStack.isTopmost(modalId)) return;
    if (isEditableTarget(e.target)) return;
    switch (e.key) {
      case "ArrowLeft":
        e.preventDefault();
        onPrev();
        return;
      case "ArrowRight":
        e.preventDefault();
        onNext();
        return;
      case "+":
      case "=":
        e.preventDefault();
        imageApi?.zoomIn();
        return;
      case "-":
      case "_":
        e.preventDefault();
        imageApi?.zoomOut();
        return;
      case "0":
        e.preventDefault();
        imageApi?.resetZoom();
        return;
      case " ":
        e.preventDefault();
        imageApi?.toggleZoom();
        return;
      case "i":
        e.preventDefault();
        infoOpen = !infoOpen;
        return;
    }
  }

  // ---- Modal stack: Add / Share modals --------------------------
  let addOpen = $state(false);
  let shareOpen = $state(false);
  let pendingMediaIds = $state<string[]>([]);
  let infoOpen = $state(false);

  function openAdd(ids: string[]) {
    pendingMediaIds = ids;
    addOpen = true;
  }
  function openShare(ids: string[]) {
    pendingMediaIds = ids;
    shareOpen = true;
  }
  async function onAdd(
    albumId: string,
  ): Promise<{ added: number; already_present: number }> {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingMediaIds } as never,
    });
    if (res.error) throw res.error;
    return res.data as { added: number; already_present: number };
  }
  async function onCreateShare(body: CreateShareBody): Promise<void> {
    const res = await api.POST("/api/v1/shares", { body: body as never });
    if (res.error) throw res.error;
  }

  // ---- Mobile breakpoint ----------------------------------------
  let isMobile = $state(false);
  $effect(() => {
    const mq = window.matchMedia("(max-width: 768px)");
    isMobile = mq.matches;
    const onChange = (e: MediaQueryListEvent) => (isMobile = e.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  });

  // Derived source for the toolbar's actions slot. Defaults to
  // "library" when there's no snapshot — the actions wrapper only
  // renders when `media` is present, so this default is rarely seen.
  const defaultSource: LightboxSource = { kind: "library" };
  const lightboxSource = $derived<LightboxSource>(
    session?.source ?? defaultSource,
  );
</script>

<svelte:window onkeydown={onKey} />

<LightboxFrame mode={fallbackMode ? "fallback" : "full"} onBackdropClick={close}>
  {#if fallbackMode}
    <LightboxToolbar onClose={close} />
  {:else}
    <LightboxToolbar onClose={close} onToggleInfo={() => (infoOpen = !infoOpen)}>
      {#snippet actions()}
        {#if media}
          <LightboxActions
            source={lightboxSource}
            {media}
            rawMedia={lastRaw}
            {mediaStore}
            {albumsStore}
            {hiddenStore}
            {toastStore}
            onAdd={openAdd}
            onShare={openShare}
            onDone={onActionDone}
          />
        {/if}
      {/snippet}
    </LightboxToolbar>
  {/if}

  {#if reconstructionInFlight}
    <div class="lb-loading"><p>Loading…</p></div>
  {:else if fallbackMode}
    {#if notFoundMode && loadError !== null}
      <div class="lb-not-found">
        <p>Photo not found.</p>
      </div>
    {:else}
      <DirectMediaDetail
        {id}
        {mediaStore}
        {albumsStore}
        {hiddenStore}
        {toastStore}
        backHref={returnHref}
        onClose={close}
      />
    {/if}
  {:else if media}
    <LightboxNavButtons
      hasPrev={nav.hasPrev}
      hasNext={nav.hasNext}
      {onPrev}
      {onNext}
    />
    <LightboxMedia
      kind="image"
      src={visibleSrc}
      alt={media.location_label ?? media.id}
      onReady={(api) => (imageApi = api)}
    />
    {#if infoOpen}
      {#if isMobile}
        <LightboxInfoSheet {media} onClose={() => (infoOpen = false)} />
      {:else}
        <LightboxInfoDrawer {media} onClose={() => (infoOpen = false)} />
      {/if}
    {/if}
  {:else}
    <div class="lb-loading"><p>Loading…</p></div>
  {/if}
</LightboxFrame>

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingMediaIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}
{#if shareOpen}
  <ShareModal
    target={{ type: "media_set", mediaIds: pendingMediaIds }}
    onCreate={onCreateShare}
    onClose={() => (shareOpen = false)}
  />
{/if}

<style>
  .lb-loading,
  .lb-not-found {
    color: white;
    padding: 2rem;
    text-align: center;
  }
</style>

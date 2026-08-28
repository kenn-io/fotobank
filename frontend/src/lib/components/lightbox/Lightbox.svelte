<!-- frontend/src/lib/components/lightbox/Lightbox.svelte -->
<!--
  Top-level lightbox shell.

  - Reads the open snapshot from lightboxSession; verifies the route
    `from` query param agrees with the snapshot's source.
  - When the snapshot matches, renders the active media's progressive
    image, prev/next buttons, toolbar, and (optionally) info drawer/sheet.
  - When the snapshot is missing or mismatched (direct entry), the
    reconstruction effect pages through the source until the active
    id is found (capped at 20 pages). Hidden 403 redirects to /hidden.
    On reconstruction success the full viewer renders with the
    reconstructed navIds; on failure it falls back to a chrome-wrapped
    DirectMediaDetail (or a 404 message).

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
  import type { AppConfigStore } from "../../app/appConfig.svelte";
  import { toMedia } from "../../media/mediaStore.svelte";
  import { lightboxSession } from "../../lightbox/lightboxSession.svelte";
  import type { LightboxSource } from "../../lightbox/lightboxSession.svelte";
  import { modalStack } from "../../lightbox/modalStack.svelte";
  import { computeNav } from "../../lightbox/lightboxNav.svelte";
  import { LightboxLoader, thumbUrl } from "../../lightbox/lightboxLoader";
  import {
    flattenLibraryIds,
    flattenSessionIds,
  } from "../../lightbox/sessionsFlatten";
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
    appConfig,
  }: {
    id: string;
    from: string;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
    appConfig: AppConfigStore;
  } = $props();

  // ---- Modal stack registration ----------------------------------
  const modalId = `lightbox-${Math.random().toString(36).slice(2)}`;
  let prevBodyOverflow = "";
  onMount(() => {
    modalStack.push({ id: modalId, onEscape: close });
    prevBodyOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
  });
  onDestroy(() => {
    modalStack.pop(modalId);
    document.body.style.overflow = prevBodyOverflow;
  });

  // ---- Source / session resolution -------------------------------
  // The snapshot is the source of truth for navIds and returnHref.
  // `from` (the route query param) must agree with the snapshot's
  // source AND the active id must be in session.navIds — otherwise
  // a stale snapshot from a prior lightbox session would be reused
  // for a fresh direct entry on the same source kind, yielding
  // wrong prev/next. When that check fails, the reconstruction
  // effect below rebuilds navIds by paging the source; only after
  // that fails do we fall through to the DirectMediaDetail fallback.
  const session = $derived(lightboxSession.snapshot);
  const fromMatchesSession = $derived.by(() => {
    if (session === null) return false;
    if (!session.navIds.includes(id)) return false;
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
      case "search": {
        if (from !== "search") return false;
        // Search is the only source that requires a qhash match: the
        // result list depends on q/filters/sort and a stale snapshot
        // from a prior query would otherwise leak (Search.svelte
        // doesn't clear lightboxSession on unmount, so a direct entry
        // to /media/:id?from=search via shared URL or browser back
        // could pick up unrelated navIds / scoreComponents). We read
        // the URL's ?qhash= directly here rather than threading it
        // through router.svelte.ts because the param is opaque (a
        // canonical-key JSON string) and isn't part of the typed
        // route surface — no other route consumes it.
        const sp = new URLSearchParams(window.location.search);
        const urlQHash = sp.get("qhash");
        if (urlQHash === null) return false;
        if (session.qHash === undefined) return false;
        return urlQHash === session.qHash;
      }
      // TODO(F4): mirror the search-source qhash pattern. A stale map
      // snapshot can leak today: user clicks photo X on /map (snapshot
      // saved with that cluster's navIds), pans away, then reopens
      // /media/X?from=map via browser-back — the old navIds get reused
      // for prev/next. Practical impact is small for v1 (slightly-wrong
      // adjacent navigation in a rare flow), so we accept the risk and
      // add the binding when /map state matures.
      case "map":
        return from === "map";
      default: {
        const _exhaustive: never = src;
        void _exhaustive;
        return false;
      }
    }
  });

  // Per-active-id score components. The search snapshot carries a
  // Map<mediaID, scoreComponents>; the active id's entry (if any) is
  // forwarded to the info drawer/sheet so LightboxMetadata can render
  // its Search relevance row. Other source kinds leave the map
  // undefined → the lookup returns undefined → the row is omitted.
  // Read only when the snapshot agrees with `from`; a stale snapshot
  // for a different source must not leak its score components into
  // the active view.
  const activeScoreComponents = $derived.by(() => {
    if (!fromMatchesSession || session === null) return undefined;
    return session.scoreComponentsById?.get(id);
  });

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
  // On id change: clear stale state, then fetch unless the visible store
  // already has a detail-shaped row. List responses omit `files`, so a
  // cached library row still needs the detail request before attachments
  // can be shown. Cancellation prevents a late response from clobbering a
  // newer one.
  $effect(() => {
    const currentId = id;
    loadError = null;
    lastRaw = null;
    if (mediaStore.get(currentId)?.files !== undefined) return;
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
  // Exception: from=map combined with a snapshot that was fetched
  // under explicit include_hidden=true is NOT a cross-context leak —
  // the user explicitly opted in via the map's Include-hidden toggle,
  // and the resulting nav set is hidden-aware.
  //
  // The `hiddenStore.unlocked` clause closes a stale-snapshot leak: if
  // hidden locks (manual lock, idle timeout, page-hide) AFTER the user
  // opened the lightbox from /map, session.includeHidden is still true
  // but the user is no longer authorized to act on hidden rows. Gating
  // on the live unlock state forces hiddenCrossContext=true in that
  // window, suppressing the Unhide button until the user re-unlocks.
  const fromMapWithHidden = $derived(
    from === "map"
      && session?.includeHidden === true
      && hiddenStore.unlocked,
  );
  const hiddenCrossContext = $derived(
    isHidden && from !== "hidden" && !fromMapWithHidden,
  );

  // Tri-state reconstruction. The effect below runs on mount and
  // synchronously sets this to "ok" (snapshot matches), "running"
  // (reconstruction kicked off), or eventually "failed" / "ok" once
  // the async pagination resolves. The "idle" seed is rewritten
  // before any user-visible render.
  type RecState = "idle" | "running" | "ok" | "failed";
  let reconstructionState = $state<RecState>("idle");

  // ---- Reconstruction effect ------------------------------------
  // When `from` doesn't agree with the snapshot, page through the
  // source until the active id appears. Capped at PAGE_CAP iterations.
  // On success, fill `reconstructed` with navIds + source so the full
  // viewer can render. On failure, flip to "failed" and the fallback
  // path takes over.
  const PAGE_CAP = 20;
  let reconstructed = $state<{
    navIds: string[];
    returnHref: string;
    source: LightboxSource;
  } | null>(null);

  type AlbumPage = {
    items?: Array<{ id: string }>;
    next_offset?: number | null;
  };

  $effect(() => {
    if (fromMatchesSession) {
      reconstructed = null;
      reconstructionState = "ok";
      return;
    }
    reconstructionState = "running";
    reconstructed = null;
    let cancelled = false;
    (async () => {
      try {
        if (from === "library" || from === "sessions") {
          let attempts = 0;
          while (
            !cancelled &&
            !mediaStore.get(id) &&
            !mediaStore.exhausted &&
            attempts < PAGE_CAP
          ) {
            await mediaStore.loadMore();
            attempts += 1;
          }
          if (cancelled) return;
          if (!mediaStore.get(id)) {
            reconstructionState = "failed";
            return;
          }
          // Order matters: Sessions groups items by session in the
          // displayed grid, while Library walks DESC library order.
          // Using the wrong helper would produce prev/next that don't
          // match the source route's ordering.
          const all =
            from === "library"
              ? flattenLibraryIds(mediaStore.months)
              : flattenSessionIds(mediaStore.months);
          reconstructed = {
            navIds: all,
            returnHref: from === "library" ? "/library" : "/sessions",
            source: { kind: from === "library" ? "library" : "sessions" },
          };
          reconstructionState = "ok";
        } else if (from.startsWith("album:")) {
          const albumId = from.slice("album:".length);
          const ids: string[] = [];
          let offset = 0;
          let pages = 0;
          let exhausted = false;
          while (
            !cancelled &&
            !ids.includes(id) &&
            !exhausted &&
            pages < PAGE_CAP
          ) {
            const resp = await fetch(
              `/api/v1/albums/${albumId}/media?offset=${offset}&limit=200`,
            );
            if (cancelled) return;
            if (!resp.ok) {
              reconstructionState = "failed";
              return;
            }
            const data = (await resp.json()) as AlbumPage;
            const items = data.items ?? [];
            for (const it of items) ids.push(it.id);
            const next = data.next_offset ?? null;
            if (next === null) {
              exhausted = true;
            } else {
              offset = next;
            }
            pages += 1;
          }
          if (cancelled) return;
          if (!ids.includes(id)) {
            reconstructionState = "failed";
            return;
          }
          reconstructed = {
            navIds: ids,
            returnHref: `/albums/${albumId}`,
            source: { kind: "album", albumId },
          };
          reconstructionState = "ok";
        } else if (from === "search") {
          // Search context isn't reconstructible from the URL alone:
          // the result list depends on the original query, filters,
          // and explain flag, none of which travel in /media/:id.
          // Fall through to the DirectMediaDetail fallback (the user
          // landed deep into a search result and the snapshot is gone).
          reconstructionState = "failed";
          return;
        } else if (from === "hidden") {
          const ids: string[] = [];
          let offset = 0;
          let pages = 0;
          let exhausted = false;
          while (
            !cancelled &&
            !ids.includes(id) &&
            !exhausted &&
            pages < PAGE_CAP
          ) {
            const resp = await fetch(
              `/api/v1/hidden/media?offset=${offset}&limit=200`,
            );
            if (cancelled) return;
            if (resp.status === 403) {
              router.navigate("/hidden", { replace: true });
              return;
            }
            if (!resp.ok) {
              reconstructionState = "failed";
              return;
            }
            const data = (await resp.json()) as AlbumPage;
            const items = data.items ?? [];
            for (const it of items) ids.push(it.id);
            const next = data.next_offset ?? null;
            if (next === null) {
              exhausted = true;
            } else {
              offset = next;
            }
            pages += 1;
          }
          if (cancelled) return;
          if (!ids.includes(id)) {
            reconstructionState = "failed";
            return;
          }
          reconstructed = {
            navIds: ids,
            returnHref: "/hidden",
            source: { kind: "hidden" },
          };
          reconstructionState = "ok";
        } else {
          reconstructionState = "failed";
        }
      } catch {
        if (cancelled) return;
        reconstructionState = "failed";
      }
    })();
    return () => {
      cancelled = true;
    };
  });

  // ---- Effective navIds / source / returnHref --------------------
  // Prefer the snapshot when it matches; otherwise read from the
  // reconstruction result. Falls back to safe defaults so the chrome
  // shell always has something to render.
  const defaultSource: LightboxSource = { kind: "library" };
  const navIds = $derived(
    fromMatchesSession && session !== null
      ? session.navIds
      : (reconstructed?.navIds ?? []),
  );
  const nav = $derived(computeNav(navIds, id));
  // returnHref defaults: when reconstruction fails or is impossible
  // (search context — see the search arm of the reconstruction
  // effect) fall back to a route that matches the `from` param when
  // possible. Otherwise /library is the safe last resort.
  const returnHref = $derived(
    fromMatchesSession && session !== null
      ? session.returnHref
      : (reconstructed?.returnHref ?? (from === "search" ? "/search" : "/library")),
  );
  const effectiveSource = $derived<LightboxSource>(
    fromMatchesSession && session !== null
      ? session.source
      : (reconstructed?.source ?? defaultSource),
  );

  const reconstructionInFlight = $derived(reconstructionState === "running");
  const reconstructionFailed = $derived(reconstructionState === "failed");
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
    // Only fire the loader when the thumb is actually available. The
    // grid-size URL would 404 for pending/working/failed/no_preview
    // rows, but loader.load publishes gridSrc to onSrc synchronously
    // before decoding, so visibleSrc would briefly become non-empty
    // and the shimmer placeholder would never show. Cancel any prior
    // load so a stale neighbor's onSrc can't land after this clear.
    if (m.thumbStatus !== "ready") {
      loader.cancelAll();
      visibleSrc = "";
      return;
    }
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
    // Skip prefetch for neighbors whose thumbs aren't ready — those
    // requests would 404 and just clog the in-flight slots.
    const prevReady = prevMedia?.thumbStatus === "ready" ? prevMedia : undefined;
    const nextReady = nextMedia?.thumbStatus === "ready" ? nextMedia : undefined;
    loader.prefetch({
      prevPreviewUrl: prevReady
        ? thumbUrl(prevReady.id, "preview", prevReady.thumbVersion)
        : null,
      nextPreviewUrl: nextReady
        ? thumbUrl(nextReady.id, "preview", nextReady.thumbVersion)
        : null,
      prevLargeUrl: prevReady
        ? thumbUrl(prevReady.id, "large", prevReady.thumbVersion)
        : null,
      nextLargeUrl: nextReady
        ? thumbUrl(nextReady.id, "large", nextReady.thumbVersion)
        : null,
    });
  });

  // ---- Navigation -----------------------------------------------
  function navTo(targetId: string) {
    // Preserve ?qhash= when paging through a search-context lightbox so
    // the snapshot validation continues to pass on the next id. Other
    // sources don't carry a qhash, so we only forward it for `from === "search"`.
    const params = new URLSearchParams();
    params.set("from", from);
    if (from === "search") {
      const sp = new URLSearchParams(window.location.search);
      const qh = sp.get("qhash");
      if (qh !== null) params.set("qhash", qh);
    }
    router.navigate(`/media/${targetId}?${params.toString()}`, {
      replace: true,
    });
  }
  function close() {
    // Don't clear lightboxSession here: SPA navigation is async, and the
    // source route's restore $effect needs to read scrollY/focus from the
    // snapshot after it remounts. clearScroll/clearReturnFocus there will
    // null out those fields once consumed; lightboxSession.open() replaces
    // the whole snapshot when the next lightbox session begins.
    router.back(returnHref);
  }
  function onPrev() {
    if (nav.prevId !== null) navTo(nav.prevId);
  }
  function onNext() {
    if (nav.nextId !== null) navTo(nav.nextId);
  }
  function onActionDone(_op: "hide" | "unhide", succeeded: string[]) {
    if (succeeded.length === 0) return;
    // LightboxActions calls onDone BEFORE pruning navIds, so `nav` still
    // reflects pre-mutation state here — capture the advance target
    // first. Reading nav.nextId AFTER the prune would always be null
    // because the active id would no longer be in navIds.
    const advanceTo = nav.nextId ?? nav.prevId;
    // Reconstructed path: prune local state. Snapshot path: LightboxActions
    // prunes lightboxSession.navIds after this callback returns.
    if (reconstructed !== null) {
      const drop = new Set(succeeded);
      reconstructed = {
        ...reconstructed,
        navIds: reconstructed.navIds.filter((nid) => !drop.has(nid)),
      };
    }
    if (advanceTo !== null) {
      navTo(advanceTo);
    } else {
      close();
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
    // Let the browser's ⌘+/⌘- (and Ctrl+/Ctrl-) page-zoom shortcuts
    // through. Without this guard, the +/- cases below preventDefault
    // every Cmd-+ press, blocking the user from zooming the whole app.
    if (e.metaKey || e.ctrlKey || e.altKey) return;
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
</script>

<svelte:window onkeydown={onKey} />

<!-- Top-level snippet: must precede the LightboxFrame element so the
     `drawer={...}` prop expression below can reference `infoDrawer` by
     name. Snippets declared inside a component element aren't in scope
     for that element's own prop expressions. -->
{#snippet infoDrawer()}
  {#if media}
    {#if activeScoreComponents}
      <LightboxInfoDrawer
        {media}
        scoreComponents={activeScoreComponents}
        onClose={() => (infoOpen = false)}
      />
    {:else}
      <LightboxInfoDrawer {media} onClose={() => (infoOpen = false)} />
    {/if}
  {/if}
{/snippet}

<LightboxFrame
  mode={fallbackMode ? "fallback" : "full"}
  onBackdropClick={close}
  drawer={!fallbackMode && media && infoOpen && !isMobile ? infoDrawer : undefined}
>
  {#if fallbackMode}
    <LightboxToolbar onClose={close} />
  {:else}
    <LightboxToolbar onClose={close} onToggleInfo={() => (infoOpen = !infoOpen)}>
      {#snippet actions()}
        {#if media}
          <LightboxActions
            source={effectiveSource}
            {media}
            rawMedia={lastRaw}
            {mediaStore}
            {albumsStore}
            {hiddenStore}
            {toastStore}
            {appConfig}
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
    {#if notFoundMode}
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
        {appConfig}
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
    {#if visibleSrc === ""}
      {#if media.thumbStatus === "failed" || media.thumbStatus === "no_preview"}
        <!-- Terminal thumb states never become ready, so a shimmer
             would mislead the user into expecting a render. Show a
             plain placeholder so they understand the preview won't
             arrive. The original file may still be openable via
             /original — but that's outside the lightbox's contract. -->
        <div class="lb-thumb-terminal" data-thumb-status={media.thumbStatus}>
          <p>Preview unavailable</p>
        </div>
      {:else}
        <!-- pending / working: thumb is in flight. Render a shimmer
             placeholder rather than letting the browser paint a
             broken-image icon for src="". The loader will set
             visibleSrc on the next successful decode and Svelte will
             swap us into LightboxMedia. -->
        <div class="lb-thumb-pending" data-thumb-status={media.thumbStatus}>
          <div class="lb-shimmer" aria-label="Photo still processing"></div>
        </div>
      {/if}
    {:else}
      <LightboxMedia
        kind="image"
        src={visibleSrc}
        alt={media.location_label ?? media.id}
        onReady={(api) => (imageApi = api)}
      />
    {/if}
    {#if infoOpen && isMobile}
      <!-- Mobile keeps the sheet as a full-stage overlay (it covers the
           image rather than reflowing it). The desktop drawer flows
           through the LightboxFrame's `drawer` snippet — see infoDrawer
           below — so the photo area shrinks instead of bleeding under. -->
      {#if activeScoreComponents}
        <LightboxInfoSheet
          {media}
          scoreComponents={activeScoreComponents}
          onClose={() => (infoOpen = false)}
        />
      {:else}
        <LightboxInfoSheet {media} onClose={() => (infoOpen = false)} />
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
{#if appConfig.sharingEnabled && shareOpen}
  <ShareModal
    target={{ type: "media_set", mediaIds: pendingMediaIds }}
    onCreate={onCreateShare}
    onClose={() => (shareOpen = false)}
  />
{/if}

<style>
  .lb-loading,
  .lb-not-found {
    color: var(--ink);
    padding: 2rem;
    text-align: center;
  }
  /* Lightbox-scale shimmer: matches MediaCell's diagonal sheen but
     covers the full lightbox stage. Reads as "in flight" without a
     spinner. Reduced-motion drops the animation but keeps the tone
     so pending vs. ready cells stay visually distinct. */
  .lb-thumb-pending,
  .lb-thumb-terminal {
    width: 80vw;
    max-width: 1200px;
    aspect-ratio: 3 / 2;
    border-radius: 6px;
    overflow: hidden;
  }
  .lb-thumb-terminal {
    background: var(--surface-2);
    color: var(--ink-3);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: var(--text-base);
  }
  .lb-thumb-terminal p { margin: 0; }
  .lb-shimmer {
    width: 100%;
    height: 100%;
    border-radius: 6px;
    background: linear-gradient(
      110deg,
      var(--surface-2) 30%,
      color-mix(in srgb, var(--surface-2) 70%, var(--ink-3)) 50%,
      var(--surface-2) 70%
    );
    background-size: 220% 100%;
    animation: lb-shimmer 1.6s linear infinite;
  }
  @keyframes lb-shimmer {
    0%   { background-position: 100% 0; }
    100% { background-position: -100% 0; }
  }
  @media (prefers-reduced-motion: reduce) {
    .lb-shimmer { animation: none; }
  }
</style>

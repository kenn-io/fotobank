<!-- frontend/src/lib/components/DirectMediaDetail.svelte -->
<script lang="ts">
  import type { MediaStore } from "../media/mediaStore.svelte";
  import { toMedia } from "../media/mediaStore.svelte";
  import { handleInternalLinkClick, router } from "../router/router.svelte";
  import { formatCoord } from "../format/coords";
  import MediaActions from "./MediaActions.svelte";
  import AddToAlbumModal from "./AddToAlbumModal.svelte";
  import ShareModal from "./ShareModal.svelte";
  import type { AlbumsStore } from "../albums/albumsStore.svelte";
  import type { HiddenStore } from "../hidden/hiddenStore.svelte";
  import type { ToastStore } from "../toasts/toastStore.svelte";
  import type { AppConfigStore } from "../app/appConfig.svelte";
  import type { CreateShareBody } from "../share/shareTypes";
  import { api } from "../api/client";

  let { id, mediaStore, albumsStore, hiddenStore, toastStore, appConfig, backHref, onClose }: {
    id: string;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
    appConfig: AppConfigStore;
    backHref?: string;
    onClose?: () => void;
  } = $props();

  // Derive from the store so mergeRaw (or any later store update) flows
  // through without a manual reassignment. `mediaStore.get(id)` reads
  // from a non-reactive Map, so we touch the reactive `months` snapshot
  // first to register a dependency — every merge replaces `months`,
  // which forces this derivation to re-run.
  let media = $derived.by(() => {
    void mediaStore.months;
    return mediaStore.get(id);
  });
  let loadError = $state<string | undefined>(undefined);

  // Raw JSON from the detail fetch. Preserved even when the media is
  // hidden (the visible MediaStore skips hidden rows, so `media` stays
  // undefined for hidden items). Used by the Unhide flow to clone the
  // raw payload with hidden_at=null before calling mergeRaw.
  let lastRaw = $state<Record<string, unknown> | undefined>(undefined);

  // Fetch detail whenever the cached row came from a list response. Detail
  // responses carry `files` (including an empty array), while lists use null.
  // Re-run when `id` changes — App.svelte mounts
  // MediaDetail without a {#key} wrapper, so navigating from one
  // /media/:id to another reuses this component instance and onMount
  // would fire only on the first mount. Capture id into the closure and
  // use a cancellation flag so a stale response can't clobber a newer
  // one if the user navigates again before the first fetch resolves.
  $effect(() => {
    const currentId = id;
    loadError = undefined;
    lastRaw = undefined;
    if (mediaStore.get(currentId)?.files !== undefined) return;

    let cancelled = false;
    (async () => {
      try {
        const resp = await api.getMedia(currentId);
        if (cancelled) return;
        if (resp.error || !resp.data) {
          loadError = `${resp.response.status}`;
          return;
        }
        const raw = resp.data;
        if (cancelled) return;
        lastRaw = raw as unknown as Record<string, unknown>;
        // Reuse the store's own JSON-adapter pathway: merge a single-item
        // array so byMediaId is also populated. The store knows how to
        // build thumbUrl from the raw row.
        // Note: if the row is hidden, mergeRaw skips it (Task 11 invariant)
        // but lastRaw preserves the data for the Unhide flow.
        mediaStore.mergeRaw([raw]);
      } catch (e) {
        if (cancelled) return;
        loadError = e instanceof Error ? e.message : "fetch failed";
      }
    })();

    return () => {
      cancelled = true;
    };
  });

  // The effective media row. For visible items, comes from the store.
  // For hidden items (when the store skips them), we reconstruct from
  // lastRaw so the template can still render metadata.
  let effectiveMedia = $derived.by((): (typeof media & { hidden_at?: string | null }) | undefined => {
    if (media) return media;
    if (!lastRaw) return undefined;
    // Normalize through toMedia so thumb_version → thumbVersion is mapped
    // correctly (finding #8). The hidden_at field passes through toMedia
    // unchanged, so the Unhide flow still works.
    const raw = lastRaw;
    const ha = raw["hidden_at"];
    if (ha !== null && typeof ha !== "string") return undefined;
    return toMedia(raw) ?? undefined;
  });

  // Whether the currently-viewed media is hidden.
  const isHidden = $derived(
    (effectiveMedia?.hidden_at != null) ||
    (lastRaw !== undefined && (lastRaw["hidden_at"] === null || typeof lastRaw["hidden_at"] === "string")
      && lastRaw["hidden_at"] !== undefined),
  );

  let previewUrl = $derived(
    effectiveMedia ? `/api/v1/media/${effectiveMedia.id}/thumb?size=preview&v=${effectiveMedia.thumbVersion ?? 0}` : "",
  );

  let imgError = $state(false);
  let lastSrc: string | undefined = undefined;
  $effect(() => {
    if (previewUrl !== lastSrc) {
      lastSrc = previewUrl;
      imgError = false;
    }
  });

  function formatTimestamp(iso: string | undefined): string {
    if (!iso) return "";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toISOString().replace("T", " ").replace(/:\d{2}\.\d{3}Z$/, " UTC");
  }

  function formatBytes(size: number | undefined): string {
    if (size === undefined || !Number.isFinite(size)) return "";
    return `${(size / 1024 / 1024).toFixed(1)} MB`;
  }

  let addOpen = $state(false);
  let shareOpen = $state(false);
  let pendingMediaIds = $state<string[]>([]);

  // The router reuses this component instance across /media/:id
  // navigations (no {#key id}), so addOpen / shareOpen / pendingMediaIds
  // would carry over from the previous photo's modal state. Reset them
  // whenever id changes so a navigation away from a photo with an open
  // modal doesn't leave that modal showing the prior photo's id.
  // `void id` registers the reactive dep without using the value (same
  // pattern as AlbumDetail.svelte's mediaStore.months access).
  $effect(() => {
    void id;
    addOpen = false;
    shareOpen = false;
    pendingMediaIds = [];
  });

  function openAdd(ids: string[]) {
    pendingMediaIds = ids;
    addOpen = true;
  }
  function openShare(ids: string[]) {
    pendingMediaIds = ids;
    shareOpen = true;
  }

  async function onAdd(albumId: string): Promise<{ added: number; already_present: number }> {
    const res = await api.addMediaToAlbum(albumId, { media_ids: pendingMediaIds });
    if (res.error) throw res.error;
    // AddToAlbumModal calls onClose() itself on success.
    return res.data as { added: number; already_present: number };
  }

  async function onCreateShare(body: CreateShareBody): Promise<void> {
    const res = await api.sharesCreate(body);
    if (res.error) throw res.error;
    // ShareModal calls onClose() itself on success — match the
    // AddToAlbumModal contract; no need to flip shareOpen here.
  }

  async function onHide(ids: string[]): Promise<void> {
    if (!window.confirm("Hide this photo?")) return;
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
      // Mark albums stale so counts refresh on next visit.
      albumsStore.markStale();
      router.navigate("/library");
      return;
    }
    const failed = result.failed ?? [];
    if (failed.length > 0) {
      toastStore.push({
        message: "Could not hide this photo.",
        details: failed.map((f) => `${f.id}: ${f.code}`),
        kind: "error",
      });
    }
  }

  async function onUnhide(ids: string[]): Promise<void> {
    if (!window.confirm("Unhide this photo?")) return;
    let result;
    try {
      result = await hiddenStore.unhide(ids);
    } catch {
      toastStore.push({ message: "Unhide failed. Try again.", kind: "error" });
      return;
    }
    const succeeded = result.succeeded ?? [];
    if (succeeded.length > 0) {
      // Clone the raw data with hidden_at=null and merge back into the
      // visible store. No navigation — user stays on this detail page.
      const raw = lastRaw ?? { id, thumb_version: 0, width: 1, height: 1 };
      mediaStore.mergeRaw([{ ...raw, hidden_at: null }]);
      // lastRaw is now stale; clear it so the derived picks up from store.
      lastRaw = undefined;
      // Mark albums stale so counts refresh.
      albumsStore.markStale();
    }
    const failed = result.failed ?? [];
    if (failed.length > 0) {
      toastStore.push({
        message: "Could not unhide this photo.",
        details: failed.map((f) => `${f.id}: ${f.code}`),
        kind: "error",
      });
    }
  }
</script>

<div class="media-detail">
  {#if onClose === undefined}
    <a
      href={backHref ?? "/library"}
      onclick={(e) => handleInternalLinkClick(e, backHref ?? "/library")}
      class="back-link"
    >
      ← Back to {backHref === undefined || backHref === "/library" ? "Library" : "previous"}
    </a>
  {/if}

  {#if loadError}
    <p class="error">Could not load media: {loadError}</p>
  {:else if !effectiveMedia}
    <p>Loading…</p>
  {:else}
    <header class="media-actions-header">
      <MediaActions
        mediaIds={[effectiveMedia.id]}
        context="media-detail"
        {isHidden}
        hiddenConfigured={hiddenStore.configured}
        {appConfig}
        onAdd={openAdd}
        onShare={openShare}
        {onHide}
        {onUnhide}
      />
    </header>
    <div class="photo">
      {#if !imgError}
        <img
          src={previewUrl}
          alt={effectiveMedia.location_label ?? effectiveMedia.id}
          onerror={() => (imgError = true)}
        />
      {:else}
        <div class="photo-placeholder">image unavailable</div>
      {/if}
    </div>

    <dl class="info">
      {#if effectiveMedia.timestamp}
        <dt>Captured</dt>
        <dd>{formatTimestamp(effectiveMedia.timestamp)}</dd>
      {/if}
      {#if effectiveMedia.location_label || (effectiveMedia.latitude != null && effectiveMedia.longitude != null)}
        <dt>Location</dt>
        <dd>
          {#if effectiveMedia.location_label}{effectiveMedia.location_label}{/if}
          {#if effectiveMedia.latitude != null && effectiveMedia.longitude != null}
            <small class="coord">{formatCoord(effectiveMedia.latitude, effectiveMedia.longitude)}</small>
          {/if}
        </dd>
      {/if}
      {#if effectiveMedia.files && effectiveMedia.files.length > 0}
        <dt>Files</dt>
        <dd class="files">
          <a href="/api/v1/media/{effectiveMedia.id}/original" download={effectiveMedia.original_filename ?? effectiveMedia.id}>
            {effectiveMedia.original_filename ?? effectiveMedia.id}
          </a>
          {#each effectiveMedia.files as file (file.id)}
            <br />
            <a
              href={`/api/v1/media/${effectiveMedia.id}/files/${file.id}/content`}
              download={file.original_filename}
            >{file.original_filename}</a>
            <span> ({file.role}, {formatBytes(file.size)})</span>
          {/each}
        </dd>
      {:else}
        <dt>Download</dt>
        <dd>
          <a href="/api/v1/media/{effectiveMedia.id}/original" download={effectiveMedia.original_filename ?? effectiveMedia.id}>
            {effectiveMedia.original_filename ?? effectiveMedia.id}
          </a>
        </dd>
      {/if}
    </dl>
  {/if}
</div>

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
  .media-detail { padding: 1rem; }
  .back-link { display: inline-block; margin-bottom: 1rem; }
  .media-actions-header { margin-bottom: 1rem; }
  .photo img { max-width: 100%; max-height: 75vh; object-fit: contain; }
  .photo-placeholder {
    border: 1px dashed currentColor; padding: 2rem; text-align: center;
  }
  .info { display: grid; grid-template-columns: max-content 1fr; gap: 0.25rem 1rem; margin-top: 1rem; }
  .info dt { font-weight: 600; }
  .info .coord { display: block; opacity: 0.7; font-size: 0.85em; }
</style>

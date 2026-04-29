<!-- frontend/src/routes/MediaDetail.svelte -->
<script lang="ts">
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import { handleInternalLinkClick } from "../lib/router/router.svelte";
  import { formatCoord } from "../lib/format/coords";
  import MediaActions from "../lib/components/MediaActions.svelte";
  import AddToAlbumModal from "../lib/components/AddToAlbumModal.svelte";
  import ShareModal from "../lib/components/ShareModal.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import type { CreateShareBody } from "../lib/share/shareTypes";
  import { api } from "../lib/api/client";

  let { id, mediaStore, albumsStore }: {
    id: string;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
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

  // Re-run the on-miss fetch every time `id` changes — App.svelte mounts
  // MediaDetail without a {#key} wrapper, so navigating from one
  // /media/:id to another reuses this component instance and onMount
  // would fire only on the first mount. Capture id into the closure and
  // use a cancellation flag so a stale response can't clobber a newer
  // one if the user navigates again before the first fetch resolves.
  $effect(() => {
    const currentId = id;
    loadError = undefined;
    if (mediaStore.get(currentId)) return;

    let cancelled = false;
    (async () => {
      try {
        const resp = await fetch(`/api/v1/media/${currentId}`);
        if (cancelled) return;
        if (!resp.ok) {
          loadError = `${resp.status}`;
          return;
        }
        const raw = await resp.json();
        if (cancelled) return;
        // Reuse the store's own JSON-adapter pathway: merge a single-item
        // array so byMediaId is also populated. The store knows how to
        // build thumbUrl from the raw row.
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

  let previewUrl = $derived(
    media ? `/api/v1/media/${media.id}/thumb?size=preview&v=${media.thumbVersion}` : "",
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
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingMediaIds } as never,
    });
    if (res.error) throw res.error;
    // AddToAlbumModal calls onClose() itself on success.
    return res.data as { added: number; already_present: number };
  }

  async function onCreateShare(body: CreateShareBody): Promise<void> {
    const res = await api.POST("/api/v1/shares", { body: body as never });
    if (res.error) throw res.error;
    // ShareModal calls onClose() itself on success — match the
    // AddToAlbumModal contract; no need to flip shareOpen here.
  }
</script>

<div class="media-detail">
  <a
    href="/library"
    onclick={(e) => handleInternalLinkClick(e, "/library")}
    class="back-link"
  >
    ← Back to Library
  </a>

  {#if loadError}
    <p class="error">Could not load media: {loadError}</p>
  {:else if !media}
    <p>Loading…</p>
  {:else if media.paired_with_id}
    <h1 class="sidecar-heading">
      RAW sidecar for
      {#if media.paired_with}
        {@const primary = media.paired_with}
        <a
          href="/media/{primary.id}"
          onclick={(e) => handleInternalLinkClick(e, `/media/${primary.id}`)}
        >
          {primary.original_filename}
        </a>
      {:else}
        <span style:opacity={0.7}>primary</span>
      {/if}
    </h1>
    <dl class="info">
      {#if media.original_filename}
        <dt>File</dt>
        <dd>{media.original_filename}</dd>
      {/if}
      {#if media.size}
        <dt>Size</dt>
        <dd>{formatBytes(media.size)}</dd>
      {/if}
      {#if media.timestamp}
        <dt>Captured</dt>
        <dd>{formatTimestamp(media.timestamp)}</dd>
      {/if}
      {#if media.location_label || (media.latitude != null && media.longitude != null)}
        <dt>Location</dt>
        <dd>
          {#if media.location_label}{media.location_label}{/if}
          {#if media.latitude != null && media.longitude != null}
            <small class="coord">{formatCoord(media.latitude, media.longitude)}</small>
          {/if}
        </dd>
      {/if}
    </dl>
    <a class="download" href="/api/v1/media/{media.id}/original" download={media.original_filename ?? media.id}>
      Download {media.original_filename ?? "file"}
    </a>
  {:else}
    <header class="media-actions-header">
      <MediaActions
        mediaIds={[media.id]}
        onAdd={openAdd}
        onShare={openShare}
      />
    </header>
    <div class="photo">
      {#if !imgError}
        <img
          src={previewUrl}
          alt={media.location_label ?? media.id}
          onerror={() => (imgError = true)}
        />
      {:else}
        <div class="photo-placeholder">image unavailable</div>
      {/if}
    </div>

    <dl class="info">
      {#if media.timestamp}
        <dt>Captured</dt>
        <dd>{formatTimestamp(media.timestamp)}</dd>
      {/if}
      {#if media.location_label || (media.latitude != null && media.longitude != null)}
        <dt>Location</dt>
        <dd>
          {#if media.location_label}{media.location_label}{/if}
          {#if media.latitude != null && media.longitude != null}
            <small class="coord">{formatCoord(media.latitude, media.longitude)}</small>
          {/if}
        </dd>
      {/if}
      {#if media.sidecars && media.sidecars.length > 0}
        <dt>Files</dt>
        <dd class="files">
          <a href="/api/v1/media/{media.id}/original" download={media.original_filename ?? media.id}>
            {media.original_filename ?? media.id}
          </a>
          {#each media.sidecars as sidecar (sidecar.id)}
            <br />
            <a href="/api/v1/media/{sidecar.id}/original" download={sidecar.original_filename ?? sidecar.id}>
              {sidecar.original_filename ?? sidecar.id}
            </a>
          {/each}
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

{#if shareOpen}
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
  .sidecar-heading { font-size: 1.25rem; margin: 0.5rem 0 0.5rem; }
  .download { display: inline-block; margin-top: 1rem; }
</style>

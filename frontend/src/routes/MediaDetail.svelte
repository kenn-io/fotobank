<!-- frontend/src/routes/MediaDetail.svelte -->
<script lang="ts">
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import { handleInternalLinkClick } from "../lib/router/router.svelte";
  import { formatCoord } from "../lib/format/coords";
  import { onMount } from "svelte";

  let { id, mediaStore }: { id: string; mediaStore: MediaStore } = $props();

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

  onMount(async () => {
    if (media) return;
    try {
      const resp = await fetch(`/api/v1/media/${id}`);
      if (!resp.ok) {
        loadError = `${resp.status}`;
        return;
      }
      const raw = await resp.json();
      // Reuse the store's own JSON-adapter pathway: merge a single-item
      // array so byMediaId is also populated. The store knows how to
      // build thumbUrl from the raw row.
      mediaStore.mergeRaw([raw]);
    } catch (e) {
      loadError = e instanceof Error ? e.message : "fetch failed";
    }
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
  {:else}
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
    </dl>
  {/if}
</div>

<style>
  .media-detail { padding: 1rem; }
  .back-link { display: inline-block; margin-bottom: 1rem; }
  .photo img { max-width: 100%; max-height: 75vh; object-fit: contain; }
  .photo-placeholder {
    border: 1px dashed currentColor; padding: 2rem; text-align: center;
  }
  .info { display: grid; grid-template-columns: max-content 1fr; gap: 0.25rem 1rem; margin-top: 1rem; }
  .info dt { font-weight: 600; }
  .info .coord { display: block; opacity: 0.7; font-size: 0.85em; }
</style>

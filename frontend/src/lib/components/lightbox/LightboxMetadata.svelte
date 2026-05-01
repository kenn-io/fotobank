<!-- frontend/src/lib/components/lightbox/LightboxMetadata.svelte -->
<script lang="ts">
  import type { Media } from "../../media/mediaStore.svelte";
  import { formatCoord } from "../../format/coords";
  import LightboxAI from "./LightboxAI.svelte";

  let { media }: { media: Media } = $props();

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
</script>

<dl class="lb-meta">
  {#if media.original_filename}
    <dt>File</dt>
    <dd>{media.original_filename}</dd>
  {/if}
  {#if media.timestamp}
    <dt>Captured</dt>
    <dd>{formatTimestamp(media.timestamp)}</dd>
  {/if}
  {#if media.size}
    <dt>Size</dt>
    <dd>{formatBytes(media.size)}</dd>
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
      <a href={`/api/v1/media/${media.id}/original`} download={media.original_filename ?? media.id}>
        {media.original_filename ?? media.id}
      </a>
      {#each media.sidecars as sc (sc.id)}
        <br />
        <a href={`/api/v1/media/${sc.id}/original`} download={sc.original_filename ?? sc.id}>
          {sc.original_filename ?? sc.id}
        </a>
      {/each}
    </dd>
  {:else}
    <dt>Download</dt>
    <dd>
      <a href={`/api/v1/media/${media.id}/original`} download={media.original_filename ?? media.id}>
        {media.original_filename ?? media.id}
      </a>
    </dd>
  {/if}
</dl>

<LightboxAI mediaId={media.id} />

<style>
  /* Inherit color from parent so this content reads correctly on
     either the dark drawer or the theme-aware bottom sheet. The
     drawer applies a white override; the sheet relies on the
     theme's default text color. */
  .lb-meta { display: grid; grid-template-columns: max-content 1fr; gap: 0.25rem 1rem; }
  .lb-meta dt { font-weight: 600; opacity: 0.7; }
  .lb-meta a { color: inherit; }
  .coord { display: block; opacity: 0.7; font-size: 0.85em; }
</style>

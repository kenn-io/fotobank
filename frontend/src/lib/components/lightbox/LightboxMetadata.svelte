<!-- frontend/src/lib/components/lightbox/LightboxMetadata.svelte -->
<script lang="ts">
  import { getDownloadMediaOriginalUrl, getDownloadMediaFileUrl } from "../../api/generated/browser";
  import type { Media } from "../../media/mediaStore.svelte";
  import type { SearchScoreComponents } from "../../search/types";
  import { formatCoord } from "../../format/coords";
  import LightboxAI from "./LightboxAI.svelte";
  import LightboxMapPin from "./LightboxMapPin.svelte";

  // scoreComponents is the V2 diagnostics payload threaded through
  // from a Search-context lightbox. When present, the Search relevance
  // row renders below the standard metadata; absent (the default for
  // every non-search caller) the row is omitted entirely. The caller
  // is responsible for not setting the prop unless explain=true was
  // requested AND the AI Inspection setting was on — both gates run
  // server-side, so a hit's missing score_components is the canonical
  // "no relevance row" signal.
  let { media, scoreComponents }: {
    media: Media;
    scoreComponents?: SearchScoreComponents;
  } = $props();

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

  // Format helpers for the Search relevance row. Matches the formats
  // used by DiagnosticsBadge so the lightbox row and the grid badge
  // render the same numbers for a given hit. Per-signal scores can be
  // null (the doc didn't match that signal); the lightbox renders an
  // em-dash placeholder rather than hiding the line so the user can
  // still see which signal contributed to the rank.
  function fmtRRF(n: number): string { return n.toFixed(4); }
  function fmtSignal(n: number | null): string {
    return n === null ? "—" : n.toFixed(2);
  }
  function fmtRank(n: number | null): string {
    return n === null ? "" : ` (rank ${n})`;
  }
</script>

{#if media.original_filename || media.timestamp || media.size}
  <div class="meta-section">
    <div class="meta-label">Capture</div>
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
        <dd class="numeric">{formatBytes(media.size)}</dd>
      {/if}
    </dl>
  </div>
{/if}

{#if media.location_label || (media.latitude != null && media.longitude != null)}
  <div class="meta-section">
    <div class="meta-label">Location</div>
    <dl class="lb-meta">
      <dt>Location</dt>
      <dd>
        {#if media.location_label}{media.location_label}{/if}
        {#if media.latitude != null && media.longitude != null}
          <small class="coord numeric">{formatCoord(media.latitude, media.longitude)}</small>
        {/if}
      </dd>
      {#if media.latitude != null && media.longitude != null}
        <dt class="map-pin-dt">Map</dt>
        <dd class="map-pin-dd">
          <LightboxMapPin {media} />
        </dd>
      {/if}
    </dl>
  </div>
{/if}

<div class="meta-section">
  <div class="meta-label">Files</div>
  <dl class="lb-meta">
    {#if media.files && media.files.length > 0}
      <dt>Files</dt>
      <dd class="files">
        <a href={getDownloadMediaOriginalUrl(media.id)} download={media.original_filename ?? media.id}>
          {media.original_filename ?? media.id}
        </a>
        {#each media.files as file (file.id)}
          <br />
          <a
            href={getDownloadMediaFileUrl(media.id, file.id)}
            download={file.original_filename}
          >{file.original_filename}</a>
          <span> ({file.role}, {formatBytes(file.size)})</span>
        {/each}
      </dd>
    {:else}
      <dt>Download</dt>
      <dd>
        <a href={getDownloadMediaOriginalUrl(media.id)} download={media.original_filename ?? media.id}>
          {media.original_filename ?? media.id}
        </a>
      </dd>
    {/if}
  </dl>
</div>

{#if scoreComponents}
  <div class="meta-section">
    <div class="meta-label">Search</div>
    <dl class="lb-meta">
      <dt>Search relevance</dt>
      <dd class="relevance" data-testid="search-relevance">
        <div><span class="rel-label">RRF</span> <span class="rel-num">{fmtRRF(scoreComponents.rrf)}</span></div>
        <div>
          <span class="rel-label">BM25</span>
          <span class="rel-num">{fmtSignal(scoreComponents.bm25)}{fmtRank(scoreComponents.rank_bm25)}</span>
        </div>
        <div>
          <span class="rel-label">Vector</span>
          <span class="rel-num">{fmtSignal(scoreComponents.vector)}{fmtRank(scoreComponents.rank_vector)}</span>
        </div>
      </dd>
    </dl>
  </div>
{/if}

<LightboxAI mediaId={media.id} />

<style>
  /* Section grouping per the darkroom mockup: each block (Capture,
     Location, Files, Search) gets its own hairline-topped section
     with a tracked uppercase label. The dl/dt/dd structure inside
     stays unchanged so test selectors and a11y semantics survive. */
  .meta-section {
    border-top: 1px solid var(--border-default);
    padding-top: var(--space-5);
    margin-bottom: var(--space-6);
  }
  .meta-section:first-of-type { border-top: none; padding-top: 0; }
  .meta-label {
    font-size: var(--font-size-2xs);
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: var(--letter-spacing-label);
    margin-bottom: 10px;
  }
  /* Inherit color from parent so this content reads correctly on
     either the dark drawer or the bottom sheet — both surfaces use
     the same dark token palette so the metadata reads cleanly with
     the default text color. */
  .lb-meta { display: grid; grid-template-columns: max-content 1fr; gap: 0.25rem 1rem; margin: 0; }
  .lb-meta dt { font-weight: 600; color: var(--text-secondary); }
  .lb-meta a { color: inherit; }
  /* Numeric/technical values render in monospace with tabular-nums so
     vertical columns (file size, coords, search relevance) line up. */
  .lb-meta .numeric {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    color: var(--text-primary);
  }
  .lb-meta .coord { display: block; color: var(--text-secondary); font-size: 0.85em; }
  .relevance {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: 0.85em;
    line-height: 1.5;
  }
  .relevance .rel-label {
    display: inline-block;
    min-width: 56px;
    color: var(--text-secondary);
  }
  .relevance .rel-num { color: var(--text-primary); }
</style>

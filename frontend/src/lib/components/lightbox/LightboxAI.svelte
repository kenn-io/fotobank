<!-- frontend/src/lib/components/lightbox/LightboxAI.svelte -->
<script lang="ts">
  import { getMediaAIView, retryPhotoAI, type AIMediaView } from "../../ai/client";
  import { handleInternalLinkClick } from "../../router/router.svelte";

  let { mediaId }: { mediaId: string } = $props();

  let view = $state<AIMediaView | null>(null);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let lastID: string | null = null;
  // Monotonic token: every load() captures a token, and only commits its
  // result when the token still matches the latest request. Without this,
  // rapid lightbox navigation (mediaId changes A → B → C while A's fetch
  // is in flight) could let A's stale response overwrite C's view.
  let loadToken = 0;

  $effect(() => {
    if (mediaId === lastID) return;
    lastID = mediaId;
    // Clear stale view on photo change so a left-over `video` skip
    // branch from the previous photo doesn't render against the new one
    // before the new fetch resolves.
    view = null;
    void load(mediaId);
  });

  async function load(id: string): Promise<void> {
    const token = ++loadToken;
    loading = true;
    loadError = null;
    try {
      const next = await getMediaAIView(id);
      if (token !== loadToken) return; // superseded by a newer load()
      view = next;
    } catch (e) {
      if (token !== loadToken) return;
      loadError = (e as Error).message;
    } finally {
      if (token === loadToken) loading = false;
    }
  }

  async function retry(task: "tag" | "caption"): Promise<void> {
    if (!mediaId) return;
    await retryPhotoAI(mediaId, task);
    await load(mediaId);
  }

  function fmtDate(s: string): string {
    return new Date(s).toLocaleString();
  }
</script>

{#if view?.skipped?.reason === "video"}
  <!-- Hide section entirely for video skips. -->
{:else if view?.skipped?.reason === "no_preview"}
  <section class="ai">
    <p class="muted">AI not available — preview unavailable</p>
  </section>
{:else if loading}
  <section class="ai"><p class="muted">Loading AI…</p></section>
{:else if loadError}
  <section class="ai"><p class="muted">AI unavailable</p></section>
{:else if view}
  <section class="ai">
    <h4 class="label">AI tags</h4>
    {#if view.tag_failure}
      <div class="failure">
        Tags failed
        <button type="button" onclick={() => retry("tag")}>Retry</button>
      </div>
    {:else if view.tags && view.tags.length > 0}
      <ul class="chips">
        {#each view.tags.slice(0, 12) as t (t.key)}
          <li class="chip">{t.label}</li>
        {/each}
        {#if view.tags.length > 12}
          <li class="more">+{view.tags.length - 12} more</li>
        {/if}
      </ul>
    {:else}
      <p class="muted">Tags pending</p>
    {/if}

    <h4 class="label">Caption</h4>
    {#if view.caption_failure}
      <div class="failure">
        Caption failed
        <button type="button" onclick={() => retry("caption")}>Retry</button>
      </div>
    {:else if view.caption}
      <p class="caption">{view.caption.text}</p>
      <p class="provenance">
        <a
          href="/settings/ai"
          onclick={(e) => handleInternalLinkClick(e, "/settings/ai")}
        >{view.caption.model_id}</a> · {fmtDate(view.caption.generated_at)}
      </p>
    {:else}
      <p class="muted">Caption pending</p>
    {/if}
  </section>
{/if}

<style>
  .ai {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px 0;
  }
  .label {
    font-size: 9px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--ink-3);
    margin: 8px 0 2px;
  }
  .chips {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .chip {
    padding: 2px 8px;
    border-radius: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    font-size: 10px;
    color: var(--ink-2);
  }
  .more {
    font-size: 10px;
    color: var(--ink-3);
    align-self: center;
  }
  .caption {
    font-size: 11px;
    color: var(--ink-2);
    margin: 0;
  }
  .provenance {
    font-size: 9px;
    color: var(--ink-3);
    margin: 2px 0 0;
  }
  .provenance a {
    text-decoration: underline;
    color: inherit;
  }
  .failure {
    font-size: 11px;
    color: var(--danger, #ef4444);
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .failure button {
    font-size: 10px;
    padding: 1px 6px;
  }
  .muted {
    font-size: 10px;
    color: var(--ink-3);
    margin: 0;
  }
</style>

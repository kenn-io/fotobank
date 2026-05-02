<!-- frontend/src/lib/search/IndexingStatusBanner.svelte
     V1: Search-page banner that explains why the engine fell back to
     BM25 (or partial hybrid). Three orthogonal banners, evaluated in
     priority order:

       1. under-80% — partial-coverage hint shown only while the user
          has typed a query and the activator hasn't promoted hybrid
          ranking yet (semanticUnavailable=false; the engine is
          ranking what it has). Suppressed once semanticUnavailable=
          true so the more specific banners take precedence.
       2. no_active_generation — semantic search has never been
          activated for this library. Dismissable per session via a
          local state flag; remounting the component (page navigation)
          resets the dismissal — this is intentional, the dismiss is
          for the current view, not a persistent preference.
       3. query_embedding_failed — the engine had an active generation
          but the per-query embedding call failed. Auto-dismisses on
          the next successful query because reason transitions back
          to "" (no per-component dismiss state needed).

     The component is presentational: it never owns search state. The
     parent route (Search.svelte) reads the search store and projects
     the four props on every mutation. -->
<script lang="ts">
  import type { SearchResponse } from "./types";

  let {
    completeness,
    semanticUnavailable,
    reason,
    hasQuery,
  }: {
    completeness: number;
    semanticUnavailable: boolean;
    reason: SearchResponse["semantic_unavailable_reason"];
    hasQuery: boolean;
  } = $props();

  // dismissed gates the no_active_generation banner only. The
  // under-80% banner has no dismiss (it's contextual to the current
  // query and disappears when completeness crosses 0.80 or when the
  // query clears); query_embedding_failed has no dismiss (auto-clears
  // when reason transitions to ""). Local $state — resets on remount,
  // which matches the plan's "per session" semantics for free
  // because Svelte routes recreate the component on navigation.
  let dismissed = $state(false);

  // Three guards, mutually exclusive in the {#if/:else if} chain.
  // showUnderEighty deliberately requires hasQuery and
  // !semanticUnavailable so the partial-coverage explanation only
  // appears when the user is actively searching and the engine is
  // hybrid-ranking. The 0.80 threshold matches the activator's
  // EmbeddingThreshold (the search engine's hybrid pivot).
  const showUnderEighty = $derived(
    hasQuery && completeness < 0.8 && !semanticUnavailable,
  );
  const showNoGen = $derived(
    reason === "no_active_generation" && !dismissed,
  );
  const showQueryEmbedFailed = $derived(
    reason === "query_embedding_failed",
  );
</script>

{#if showUnderEighty}
  <div class="banner" data-testid="indexing-status-banner-under-eighty">
    Search is still indexing — semantic ranking covers
    {Math.round(completeness * 100)}% of your library so far.
    Lexical results below.
  </div>
{:else if showNoGen}
  <div class="banner" data-testid="indexing-status-banner-no-gen">
    Semantic search is not yet available — your library is still being
    indexed for the first time.
    <button
      type="button"
      class="dismiss"
      data-testid="indexing-status-banner-dismiss"
      onclick={() => (dismissed = true)}
    >Dismiss</button>
  </div>
{:else if showQueryEmbedFailed}
  <div class="banner" data-testid="indexing-status-banner-query-failed">
    Semantic ranking is temporarily unavailable. Showing lexical
    results.
  </div>
{/if}

<style>
  .banner {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 8px 0;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg-elevated);
    color: var(--text-secondary);
    font-size: 13px;
    line-height: 1.4;
  }
  .dismiss {
    margin-left: auto;
    height: 24px;
    padding: 0 8px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: 12px;
    cursor: pointer;
  }
</style>

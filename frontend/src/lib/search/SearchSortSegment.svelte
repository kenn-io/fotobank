<!-- frontend/src/lib/search/SearchSortSegment.svelte
     U2: segmented control over the SearchSort union. The selected
     button mirrors the *effective* sort the backend would apply: when
     sort="relevance" and the query is empty, the engine coerces to
     newest (no relevance ranking is possible without a query), so the
     segment shows Newest as selected to match what the user sees.
     The user can still click Relevance — onChange fires with the
     literal click target and the store records the choice — but the
     visual selection stays Newest until the query is non-empty.

     Like the rest of the search surface, this component is
     presentational: it never owns sort state. The parent route holds
     the SearchSort and re-renders this component when it changes.
     Mutations bubble up via the onChange callback (Svelte 5 idiom).
-->
<script lang="ts">
  import type { SearchSort } from "./types";

  // Tests render the segment in isolation; production callers pass the
  // store's `query` so the relevance→newest coercion lines up. onChange
  // is optional so the component can be mounted standalone without
  // wiring a sink (storybook, /search idle state).
  let { sort, onChange, query = "" }: {
    sort: SearchSort;
    onChange?: (next: SearchSort) => void;
    query?: string;
  } = $props();

  // emit centralises the optional onChange — callers without a sink
  // (storybook, isolated render) get a silent no-op.
  function emit(next: SearchSort): void {
    onChange?.(next);
  }

  // effectiveSelected mirrors the backend's effective_sort coercion:
  // relevance + empty query → newest. We compute it here (rather than
  // reading store.effectiveSort) because the segment is decoupled from
  // the store and only sees its own props. The behaviour matches the
  // engine's M3 routing rule.
  const effectiveSelected = $derived<SearchSort>(
    sort === "relevance" && query === "" ? "newest" : sort,
  );
</script>

<div class="search-sort-segment segmented" role="radiogroup" aria-label="Sort">
  <button
    type="button"
    class:selected={effectiveSelected === "relevance"}
    aria-pressed={effectiveSelected === "relevance"}
    data-testid="search-sort-relevance"
    onclick={() => emit("relevance")}
  >Relevance</button>
  <button
    type="button"
    class:selected={effectiveSelected === "newest"}
    aria-pressed={effectiveSelected === "newest"}
    data-testid="search-sort-newest"
    onclick={() => emit("newest")}
  >Newest</button>
  <button
    type="button"
    class:selected={effectiveSelected === "oldest"}
    aria-pressed={effectiveSelected === "oldest"}
    data-testid="search-sort-oldest"
    onclick={() => emit("oldest")}
  >Oldest</button>
</div>

<style>
  .search-sort-segment {
    display: inline-flex;
  }
  .search-sort-segment button {
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: 13px;
    cursor: pointer;
  }
  .search-sort-segment button:not(:first-child) {
    border-left: none;
  }
  .search-sort-segment button:first-child {
    border-radius: var(--radius) 0 0 var(--radius);
  }
  .search-sort-segment button:last-child {
    border-radius: 0 var(--radius) var(--radius) 0;
  }
  .search-sort-segment button.selected {
    background: var(--bg-elevated);
    font-weight: 600;
  }
</style>

<script lang="ts">
  type Item = { value: string; count: number; selected: boolean; label?: string };

  let {
    items,
    onToggle,
    searchPlaceholder = "Search…",
    searchThreshold = 8,
  }: {
    items: Item[];
    onToggle: (value: string) => void;
    searchPlaceholder?: string;
    searchThreshold?: number;
  } = $props();

  let query = $state("");
  const showSearch = $derived(items.length > searchThreshold);
  const filtered = $derived.by((): Item[] => {
    // When the search input is hidden (items dropped at/below
    // threshold) the user has no way to clear a stale query. Ignore
    // it so the list always reflects the current items as-is. Resetting
    // `query` here would race the parent's reactive read; gating the
    // filter is the simpler invariant.
    if (!showSearch) return items;
    const q = query.trim().toLowerCase();
    if (q === "") return items;
    return items.filter((it) =>
      (it.label ?? it.value).toLowerCase().includes(q),
    );
  });
</script>

<div class="facet-list">
  {#if showSearch}
    <input
      class="facet-search"
      type="text"
      placeholder={searchPlaceholder}
      bind:value={query}
    />
  {/if}
  <div class="rows">
    {#each filtered as it (it.value)}
      <button
        type="button"
        class="row"
        class:selected={it.selected}
        role="checkbox"
        aria-checked={it.selected}
        aria-label={it.label ?? it.value}
        onclick={() => onToggle(it.value)}
      >
        <span class="name">{it.label ?? it.value}</span>
        <span class="leader" aria-hidden="true"></span>
        <span class="count">{it.count}</span>
      </button>
    {:else}
      <div class="empty">{query ? "No matches" : "No items"}</div>
    {/each}
  </div>
</div>

<style>
  .facet-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  /* Baseline-only input: no box, no fill. The bottom 1px border
     thickens to 2px amber on focus, with padding-bottom adjusting
     -1px so the text doesn't shift downward when the rule grows. */
  .facet-search {
    width: 100%;
    padding: var(--space-2) 0;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border-default);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--font-size-xs);
    outline: none;
  }
  .facet-search:focus {
    border-bottom: 2px solid var(--accent-blue);
    padding-bottom: calc(var(--space-2) - 1px);
  }
  .facet-search::placeholder {
    font-family: var(--font-sans);
    font-weight: 400;
    color: var(--fb-text-faint);
  }
  .rows {
    display: flex;
    flex-direction: column;
    max-height: 220px;
    overflow-y: auto;
  }
  /* Frame-mark selection: a 2px amber bar on the left edge,
     drawn via inset box-shadow so selection doesn't shift layout. */
  .row {
    display: flex;
    align-items: baseline;
    gap: 0;
    width: 100%;
    height: 28px;
    padding: var(--space-2) var(--space-3) var(--space-2) var(--space-4);
    background: transparent;
    color: var(--text-secondary);
    border: 0;
    box-shadow: inset 2px 0 0 0 transparent;
    text-align: left;
    cursor: pointer;
    font-family: var(--font-sans);
    font-size: var(--font-size-xs);
    transition:
      background 100ms,
      color 100ms,
      box-shadow 100ms;
  }
  .row:hover { color: var(--text-primary); }
  .row.selected {
    color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 6%, transparent);
    box-shadow: inset 2px 0 0 0 var(--accent-blue);
  }
  .name {
    flex: 0 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* Dot-leader between name and count. The translateY tweak lifts
     the dotted line up to the typographic baseline of the surrounding
     text instead of sitting on the descender line. */
  .leader {
    flex: 1;
    min-width: var(--space-3);
    align-self: baseline;
    border-bottom: 1px dotted var(--fb-text-faint);
    margin: 0 var(--space-3);
    transform: translateY(-3px);
  }
  .count {
    flex: 0 0 auto;
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
  }
  .row.selected .count { color: var(--accent-blue); }
  .empty {
    padding: var(--space-5) 0;
    color: var(--text-muted);
    font-family: var(--font-sans);
    font-size: var(--font-size-xs);
    text-align: center;
  }
</style>

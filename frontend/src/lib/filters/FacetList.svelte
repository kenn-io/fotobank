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
        onclick={() => onToggle(it.value)}
      >
        <span class="check" class:on={it.selected}>
          {#if it.selected}
            <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true">
              <path
                d="M1.5 4 L3.2 5.8 L6.5 2.2"
                fill="none"
                stroke="white"
                stroke-width="1.2"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          {/if}
        </span>
        <span class="name">{it.label ?? it.value}</span>
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
    gap: var(--space-2);
  }
  .facet-search {
    width: 100%;
    padding: var(--space-2) var(--space-3);
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--ink);
    font-size: var(--text-sm);
    outline: none;
  }
  .facet-search:focus {
    border-color: var(--amber);
  }
  .facet-search::placeholder {
    color: var(--ink-4);
  }
  .rows {
    display: flex;
    flex-direction: column;
    max-height: 220px;
    overflow-y: auto;
    border: 1px solid var(--border);
  }
  .row {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    width: 100%;
    padding: var(--space-2) var(--space-3);
    background: transparent;
    color: var(--ink-2);
    border: 0;
    border-bottom: 1px solid var(--border);
    text-align: left;
    cursor: pointer;
    font-size: var(--text-sm);
    transition:
      background 100ms,
      color 100ms;
  }
  .row:last-child {
    border-bottom: 0;
  }
  .row:hover {
    background: var(--surface-2);
    color: var(--ink);
  }
  .row.selected {
    color: var(--amber);
  }
  .check {
    width: 12px;
    height: 12px;
    border: 1px solid var(--border-2);
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .check.on {
    background: var(--amber);
    border-color: var(--amber);
  }
  .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .count {
    flex-shrink: 0;
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: var(--text-xs);
    color: var(--ink-4);
  }
  .empty {
    padding: var(--space-3);
    color: var(--ink-4);
    font-size: var(--text-sm);
    text-align: center;
  }
</style>

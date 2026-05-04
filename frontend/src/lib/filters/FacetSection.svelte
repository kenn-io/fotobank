<script lang="ts">
  import FacetList from "./FacetList.svelte";

  type Item = {
    value: string;
    count: number;
    selected: boolean;
    label?: string;
  };

  let {
    label,
    totalCount,
    items,
    onToggle,
    storageKey,
    expandedDefault = true,
    searchPlaceholder = "Search…",
  }: {
    label: string;
    totalCount: number;
    items: Item[];
    onToggle: (value: string) => void;
    storageKey: string;
    expandedDefault?: boolean;
    searchPlaceholder?: string;
  } = $props();

  function readInitial(): boolean {
    if (typeof localStorage === "undefined") return expandedDefault;
    const v = localStorage.getItem(storageKey);
    if (v === "expanded") return true;
    if (v === "collapsed") return false;
    return expandedDefault;
  }

  let expanded = $state(readInitial());
  const activeCount = $derived(items.filter((i) => i.selected).length);

  function toggle() {
    expanded = !expanded;
    if (typeof localStorage !== "undefined") {
      localStorage.setItem(storageKey, expanded ? "expanded" : "collapsed");
    }
  }
</script>

<div class="facet-section">
  <button
    type="button"
    class="header"
    aria-expanded={expanded}
    onclick={toggle}
  >
    <span class="chevron" class:expanded>▸</span>
    <span class="label">{label}</span>
    {#if !expanded && activeCount > 0}
      <span class="active-badge">{activeCount}</span>
    {/if}
    <span class="total">{totalCount}</span>
  </button>
  {#if expanded}
    <div class="body">
      <FacetList {items} {onToggle} {searchPlaceholder} />
    </div>
  {/if}
</div>

<style>
  .facet-section + :global(.facet-section) {
    margin-top: var(--space-3);
  }
  .header {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    width: 100%;
    padding: var(--space-2) 0;
    background: transparent;
    border: 0;
    color: var(--ink-3);
    font-size: var(--text-xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: var(--label-track);
    cursor: pointer;
    transition: color 100ms;
  }
  .header:hover {
    color: var(--ink);
  }
  .chevron {
    display: inline-block;
    width: 10px;
    transition: transform 120ms;
    color: var(--ink-4);
  }
  .chevron.expanded {
    transform: rotate(90deg);
  }
  .label {
    flex: 1;
    text-align: left;
  }
  .active-badge {
    background: var(--amber);
    color: var(--bg);
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: 10px;
    padding: 0 5px;
    border-radius: 8px;
    min-width: 16px;
    text-align: center;
    line-height: 14px;
  }
  .total {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: 11px;
    color: var(--ink-4);
  }
  .body {
    margin-top: var(--space-2);
  }
</style>

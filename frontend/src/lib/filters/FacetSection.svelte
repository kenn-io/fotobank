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
    class:expanded
    aria-expanded={expanded}
    onclick={toggle}
  >
    <span class="rule" aria-hidden="true"></span>
    <span class="label">{label}</span>
    <span class="right">
      {#if !expanded && activeCount > 0}
        <span class="active-badge">{activeCount}</span>
      {/if}
      <span class="total">{totalCount}</span>
    </span>
  </button>
  {#if expanded}
    <div class="body">
      <FacetList {items} {onToggle} {searchPlaceholder} />
    </div>
  {/if}
</div>

<style>
  .facet-section + :global(.facet-section) {
    margin-top: var(--space-5);
  }
  .header {
    position: relative;
    display: flex;
    align-items: center;
    width: 100%;
    padding: var(--space-2) 0;
    min-height: 22px;
    background: transparent;
    border: 0;
    color: var(--text-primary);
    cursor: pointer;
  }
  /* Rule runs full width behind the label + right group; both
     paint var(--bg-primary) so the line is interrupted on the masked edges
     ("── cameras ──────────  845"). */
  .rule {
    position: absolute;
    left: 0;
    right: 0;
    top: 50%;
    border-top: 1px solid var(--border-default);
    pointer-events: none;
    transition: border-top-color 100ms;
  }
  .header:hover .rule { border-top-color: var(--text-muted); }
  /* Label text starts at x=var(--space-4) (8px) so it aligns with
     FacetList rows, which use padding-left: var(--space-4). The label
     gets symmetric inline padding (8px L + R) so the bg interrupting
     the rule looks balanced around the label text. */
  .label {
    position: relative;
    z-index: 1;
    padding: 0 var(--space-4);
    background: var(--bg-primary);
    font-family: var(--font-mono);
    font-weight: 500;
    font-size: var(--font-size-2xs);
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: var(--letter-spacing-label);
    transition: color 100ms;
  }
  .header.expanded .label { color: var(--text-primary); }
  .right {
    position: relative;
    z-index: 1;
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: var(--space-3);
    padding-left: var(--space-3);
    /* Match rows' padding-right (var(--space-3) on .row) so the
       header's total count and the per-row counts share a vertical
       guideline at the right edge. */
    padding-right: var(--space-3);
    background: var(--bg-primary);
  }
  /* Bracketed mono token "[2]" — brackets are pseudo-element
     content so the textContent of .active-badge stays "2" and
     getByText(activeCount) keeps matching. */
  .active-badge {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: var(--font-size-2xs);
    color: var(--accent-blue);
  }
  .active-badge::before { content: "["; color: var(--accent-blue); }
  .active-badge::after  { content: "]"; color: var(--accent-blue); }
  .total {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
  }
  .body {
    padding-block: var(--space-3);
  }
</style>

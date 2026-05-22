<!-- frontend/src/lib/search/SearchFilterChips.svelte
     U1: removable chip strip rendered below the popover. Each active
     filter on `filters` produces one chip with a remove (×) button;
     the parent route owns the SearchFilters and re-renders this
     component with the chip absent after onChange fires.

     Chips are presentational; the popover is the source of truth for
     values. We deliberately don't make a chip *click* re-open the
     popover (the popover renders inline in v1), but we do guarantee
     the × button on every chip dispatches the right onChange so chips
     are usable on their own. -->
<script lang="ts">
  import type { SearchFilters } from "./types";

  let { filters, onChange }: {
    filters: SearchFilters;
    onChange?: (next: SearchFilters) => void;
  } = $props();

  // emit centralises the optional onChange — callers without a sink
  // (storybook, isolated render) get a silent no-op.
  function emit(next: SearchFilters): void {
    onChange?.(next);
  }

  function removeDateAfter(): void {
    const next: SearchFilters = { ...filters };
    delete next.dateAfter;
    emit(next);
  }

  function removeDateBefore(): void {
    const next: SearchFilters = { ...filters };
    delete next.dateBefore;
    emit(next);
  }

  function removeTag(tagKey: string): void {
    emit({ ...filters, tags: filters.tags.filter((t) => t.tag_key !== tagKey) });
  }

  function removeLocation(): void {
    const next: SearchFilters = { ...filters };
    delete next.location;
    emit(next);
  }

  function removeIncludeHidden(): void {
    const next: SearchFilters = { ...filters };
    delete next.includeHidden;
    emit(next);
  }
</script>

<div class="search-filter-chips" data-testid="search-filter-chips">
  {#if filters.dateAfter !== undefined && filters.dateAfter !== ""}
    <span class="chip" data-testid="chip-date-after">
      <span class="chip-text">After {filters.dateAfter}</span>
      <button
        type="button"
        class="chip-remove"
        data-testid="chip-remove"
        aria-label="Remove after-date filter"
        onclick={removeDateAfter}
      >×</button>
    </span>
  {/if}
  {#if filters.dateBefore !== undefined && filters.dateBefore !== ""}
    <span class="chip" data-testid="chip-date-before">
      <span class="chip-text">Before {filters.dateBefore}</span>
      <button
        type="button"
        class="chip-remove"
        data-testid="chip-remove"
        aria-label="Remove before-date filter"
        onclick={removeDateBefore}
      >×</button>
    </span>
  {/if}
  {#each filters.tags as tag (tag.tag_key)}
    <span class="chip" data-testid={`chip-tag-${tag.tag_key}`}>
      <span class="chip-text">{tag.tag_label}</span>
      <button
        type="button"
        class="chip-remove"
        data-testid="chip-remove"
        aria-label={`Remove ${tag.tag_label} filter`}
        onclick={() => removeTag(tag.tag_key)}
      >×</button>
    </span>
  {/each}
  {#if filters.location !== undefined && filters.location.location_label !== ""}
    <span class="chip" data-testid="chip-location">
      <span class="chip-text">{filters.location.location_label}</span>
      <button
        type="button"
        class="chip-remove"
        data-testid="chip-remove"
        aria-label="Remove location filter"
        onclick={removeLocation}
      >×</button>
    </span>
  {/if}
  {#if filters.includeHidden === true}
    <span class="chip" data-testid="chip-include-hidden">
      <span class="chip-text">Include hidden</span>
      <button
        type="button"
        class="chip-remove"
        data-testid="chip-remove"
        aria-label="Remove include-hidden filter"
        onclick={removeIncludeHidden}
      >×</button>
    </span>
  {/if}
</div>

<style>
  .search-filter-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 6px 0;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 4px 2px 8px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
    color: var(--ink);
    font-size: 12px;
  }
  .chip-text {
    line-height: 16px;
  }
  .chip-remove {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    padding: 0;
    border: none;
    border-radius: 50%;
    background: transparent;
    color: var(--ink-2);
    font-size: 14px;
    line-height: 1;
    cursor: pointer;
  }
  .chip-remove:hover {
    background: var(--surface-2);
    color: var(--ink);
  }
</style>

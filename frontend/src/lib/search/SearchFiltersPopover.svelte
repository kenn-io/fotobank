<!-- frontend/src/lib/search/SearchFiltersPopover.svelte
     U1: filter surface that surfaces date range, tag autocomplete,
     location autocomplete, and a media-type segmented control. The
     component renders inline (popover-as-button is a future iteration);
     it never owns filter state — the parent route holds the
     SearchFilters and re-renders this component when they change.
     Mutations bubble up via the onChange callback (Svelte 5 idiom).
     -->
<script lang="ts">
  import {
    Checkbox,
    SegmentedControl,
    type SegmentedControlOption,
  } from "@kenn-io/kit-ui";
  import type { SearchClient } from "./client";
  import { searchClient as defaultClient } from "./client";
  import type { SearchFilters } from "./types";

  // Tests inject a stub client; production callers omit it and the
  // popover uses the same singleton the rest of the search surface
  // shares. onChange is optional so the component can be mounted in
  // isolation (storybook, /search idle state) without wiring a sink.
  let { filters, onChange, client = defaultClient }: {
    filters: SearchFilters;
    onChange?: (next: SearchFilters) => void;
    client?: SearchClient;
  } = $props();

  // emit centralises the onChange call so the no-sink case is a single
  // guard rather than scattered `?.()` callsites.
  function emit(next: SearchFilters): void {
    onChange?.(next);
  }

  // Date inputs read directly from the filters prop (one-way `value=`
  // binding, not bind:value) and commit changes through onChange.
  // Holding a separate internal copy would force us to either (a)
  // duplicate prop-sync logic via $effect or (b) silently drift from
  // the parent's view of state. The parent re-renders the popover with
  // the updated filters after onChange flips the store, and the
  // `value` attribute follows.

  // Tag autocomplete state. tagSuggestions is the rendered list; the
  // wire shape is {key, label, count} (matches autocompleteTagsBody).
  // We don't expose the typed input as a chip — only a clicked
  // suggestion adds to filters.tags so we always carry the canonical
  // tag_key, never the user's free text.
  let tagInput = $state("");
  let tagSuggestions = $state<{ key: string; label: string; count: number }[]>([]);

  // Location autocomplete state. Locations have no canonical key — the
  // engine matches on the label directly — so the wire shape is
  // (label, count) only.
  let locationInput = $state("");
  let locationSuggestions = $state<{ label: string; count: number }[]>([]);

  // Debounced autocomplete: a single shared timer per input avoids
  // hammering the backend on every keystroke. AUTOCOMPLETE_DEBOUNCE_MS
  // matches the AppHeader's pattern; 200 ms is short enough to feel
  // immediate but long enough to coalesce typing bursts.
  const AUTOCOMPLETE_DEBOUNCE_MS = 200;
  let tagTimer: ReturnType<typeof setTimeout> | undefined;
  let locTimer: ReturnType<typeof setTimeout> | undefined;

  // Token-based race protection: every input change bumps the token
  // synchronously. Both the dispatch (post-debounce) and the response
  // handler check that the captured token is still current; either gate
  // failing means the user has typed (or cleared) since this request
  // was queued, so its response must be dropped. Without bumping
  // synchronously on every input change, a slow "do" response landing
  // after the user has cleared the input would repopulate suggestions
  // for an empty input.
  let tagFetchToken = 0;
  let locFetchToken = 0;

  const mediaTypeOptions: SegmentedControlOption[] = [
    { value: "all", label: "All" },
    { value: "photo", label: "Photos" },
    { value: "video", label: "Videos" },
  ];

  $effect(() => {
    const value = tagInput;
    tagFetchToken++;
    const myToken = tagFetchToken;
    if (tagTimer !== undefined) clearTimeout(tagTimer);
    if (value.length < 1) {
      tagSuggestions = [];
      return;
    }
    tagTimer = setTimeout(() => {
      if (myToken !== tagFetchToken) return;
      void client.autocompleteTags({ prefix: value }).then((res) => {
        if (myToken !== tagFetchToken) return;
        tagSuggestions = res.tags;
      });
    }, AUTOCOMPLETE_DEBOUNCE_MS);
  });

  $effect(() => {
    const value = locationInput;
    locFetchToken++;
    const myToken = locFetchToken;
    if (locTimer !== undefined) clearTimeout(locTimer);
    if (value.length < 1) {
      locationSuggestions = [];
      return;
    }
    locTimer = setTimeout(() => {
      if (myToken !== locFetchToken) return;
      void client.autocompleteLocations({ substring: value }).then((res) => {
        if (myToken !== locFetchToken) return;
        locationSuggestions = res.locations;
      });
    }, AUTOCOMPLETE_DEBOUNCE_MS);
  });

  // Cleanup pending timers on unmount so a debounced fetch can't fire
  // after the component is gone (would write into a detached state).
  $effect(() => {
    return () => {
      if (tagTimer !== undefined) clearTimeout(tagTimer);
      if (locTimer !== undefined) clearTimeout(locTimer);
    };
  });

  function onDateAfterInput(ev: Event): void {
    const v = (ev.target as HTMLInputElement).value;
    const next: SearchFilters = { ...filters };
    if (v === "") {
      delete next.dateAfter;
    } else {
      next.dateAfter = v;
    }
    emit(next);
  }

  function onDateBeforeInput(ev: Event): void {
    const v = (ev.target as HTMLInputElement).value;
    const next: SearchFilters = { ...filters };
    if (v === "") {
      delete next.dateBefore;
    } else {
      next.dateBefore = v;
    }
    emit(next);
  }

  // addTag commits a clicked suggestion as a chip on filters.tags. We
  // dedupe by tag_key so clicking the same suggestion twice doesn't
  // produce a duplicate chip (the backend would normalize this anyway,
  // but a duplicate chip on the UI side looks broken).
  function addTag(s: { key: string; label: string; count: number }): void {
    const exists = filters.tags.some((t) => t.tag_key === s.key);
    const tags = exists
      ? filters.tags
      : [...filters.tags, { tag_key: s.key, tag_label: s.label }];
    emit({ ...filters, tags });
    tagInput = "";
    tagSuggestions = [];
  }

  function setLocation(s: { label: string; count: number }): void {
    emit({ ...filters, location: { location_label: s.label } });
    locationInput = "";
    locationSuggestions = [];
  }

  function setMediaType(value: string): void {
    const next: SearchFilters = { ...filters };
    if (value === "all") {
      delete next.mediaType;
    } else if (value === "photo" || value === "video") {
      next.mediaType = value;
    } else {
      return;
    }
    emit(next);
  }

  // toggleIncludeHidden flips the includeHidden flag on the filters.
  // The toggle is opt-in: when checked the request asks the backend to
  // include hidden media in results; when unchecked the field is
  // removed entirely so the wire shape drops it (the engine's default
  // is exclusion).
  function toggleIncludeHidden(checked: boolean): void {
    const next: SearchFilters = { ...filters };
    if (checked) {
      next.includeHidden = true;
    } else {
      delete next.includeHidden;
    }
    emit(next);
  }
</script>

<div class="filters-popover" data-testid="search-filters-popover">
  <div class="filter-row">
    <label class="filter-label">
      <span>After</span>
      <input
        type="date"
        data-testid="search-filter-date-after"
        value={filters.dateAfter ?? ""}
        oninput={onDateAfterInput}
      />
    </label>
    <label class="filter-label">
      <span>Before</span>
      <input
        type="date"
        data-testid="search-filter-date-before"
        value={filters.dateBefore ?? ""}
        oninput={onDateBeforeInput}
      />
    </label>
  </div>

  <div class="filter-row">
    <label class="filter-label">
      <span>Tag</span>
      <input
        type="text"
        data-testid="search-filter-tag-input"
        placeholder="Type to search tags…"
        bind:value={tagInput}
      />
    </label>
    <ul class="suggestions" data-testid="search-filter-tag-suggestions">
      {#each tagSuggestions as s (s.key)}
        <li>
          <button
            type="button"
            class="suggestion"
            data-testid={`search-filter-tag-suggestion-${s.key}`}
            onclick={() => addTag(s)}
          >
            <span class="suggestion-label">{s.label}</span>
            <span class="suggestion-count">{s.count}</span>
          </button>
        </li>
      {/each}
    </ul>
  </div>

  <div class="filter-row">
    <label class="filter-label">
      <span>Location</span>
      <input
        type="text"
        data-testid="search-filter-location-input"
        placeholder="Type to search locations…"
        bind:value={locationInput}
      />
    </label>
    <ul class="suggestions" data-testid="search-filter-location-suggestions">
      {#each locationSuggestions as s (s.label)}
        <li>
          <button
            type="button"
            class="suggestion"
            data-testid={`search-filter-location-suggestion-${s.label}`}
            onclick={() => setLocation(s)}
          >
            <span class="suggestion-label">{s.label}</span>
            <span class="suggestion-count">{s.count}</span>
          </button>
        </li>
      {/each}
    </ul>
  </div>

  <div class="filter-row">
    <SegmentedControl
      options={mediaTypeOptions}
      value={filters.mediaType ?? "all"}
      onchange={setMediaType}
      ariaLabel="Media type"
      variant="borderless"
      block
    />
  </div>

  <div class="filter-row">
    <Checkbox
      checked={filters.includeHidden === true}
      onchange={toggleIncludeHidden}
      label="Include hidden media"
    />
  </div>
</div>

<style>
  .filters-popover {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 8px 12px;
    border: 1px solid var(--border-default);
    background: var(--bg-inset);
  }
  .filter-row {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .filter-label {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 12px;
    color: var(--text-secondary);
  }
  .filter-label input {
    height: 26px;
    padding: 0 8px;
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: 13px;
  }
  .suggestions {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
  }
  .suggestion {
    display: flex;
    justify-content: space-between;
    align-items: center;
    width: 100%;
    padding: 4px 8px;
    border: none;
    background: transparent;
    color: var(--text-primary);
    font-size: 13px;
    text-align: left;
    cursor: pointer;
  }
  .suggestion:hover {
    background: var(--bg-surface);
  }
  .suggestion-count {
    color: var(--text-secondary);
    font-size: 11px;
  }
</style>

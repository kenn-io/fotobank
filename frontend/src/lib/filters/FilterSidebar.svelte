<script lang="ts">
  import FacetSection from "./FacetSection.svelte";
  import { withToggled, isEmpty, type ActiveFilters, type FacetGroup } from "./activeFilters";
  import type { FacetsResponse } from "./facetsStore.svelte";

  let {
    route,
    filters,
    response,
    onChange,
  }: {
    route: "library" | "search" | "map";
    filters: ActiveFilters;
    response: FacetsResponse | null;
    onChange: (next: ActiveFilters) => void;
  } = $props();

  const cameraItems = $derived(
    (response?.cameras ?? []).map((c) => ({
      value: c.value,
      count: c.count,
      selected: filters.cameras.includes(c.value),
    })),
  );
  const lensItems = $derived(
    (response?.lenses ?? []).map((l) => ({
      value: l.value,
      count: l.count,
      selected: filters.lenses.includes(l.value),
    })),
  );
  const tagItems = $derived(
    (response?.tags ?? []).map((t) => ({
      value: t.key,
      label: t.label,
      count: t.count,
      selected: filters.tagKeys.includes(t.key),
    })),
  );
  const placesItems = $derived(
    response
      ? [
          { value: "true", label: "Has GPS", count: response.places.with_gps,
            selected: filters.hasGps === true },
          { value: "false", label: "No GPS", count: response.places.without_gps,
            selected: filters.hasGps === false },
        ]
      : [],
  );
  const mediaTypeItems = $derived(
    (response?.media_types ?? []).map((m) => ({
      value: m.value,
      label: m.value === "photo" ? "Photo" : "Video",
      count: m.count,
      selected: filters.mediaType === m.value,
    })),
  );

  const cameraTotal = $derived(cameraItems.reduce((n, i) => n + i.count, 0));
  const lensTotal   = $derived(lensItems.reduce((n, i) => n + i.count, 0));
  const tagTotal    = $derived(tagItems.reduce((n, i) => n + i.count, 0));
  const placesTotal = $derived((response?.places.with_gps ?? 0) + (response?.places.without_gps ?? 0));
  const mtTotal     = $derived(mediaTypeItems.reduce((n, i) => n + i.count, 0));

  function toggle(group: FacetGroup, value: string) {
    onChange(withToggled(filters, group, value));
  }

  function clearAll() {
    onChange({ cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null });
  }
</script>

<div class="filter-sidebar">
  <div class="group-header">
    <span class="rule" aria-hidden="true"></span>
    <span class="title">
      <span class="title-display" aria-hidden="true">index</span>
      <span class="title-a11y">FILTERS</span>
    </span>
    {#if !isEmpty(filters)}
      <button type="button" class="clear" onclick={clearAll}>
        clear <span class="clear-glyph" aria-hidden="true">&#x21B5;</span>
      </button>
    {/if}
  </div>

  <FacetSection
    label="Cameras"
    totalCount={cameraTotal}
    items={cameraItems}
    onToggle={(v) => toggle("cameras", v)}
    storageKey="fotobank:facet:cameras"
    searchPlaceholder="search cameras…"
  />
  <FacetSection
    label="Lenses"
    totalCount={lensTotal}
    items={lensItems}
    onToggle={(v) => toggle("lenses", v)}
    storageKey="fotobank:facet:lenses"
    searchPlaceholder="search lenses…"
  />
  <FacetSection
    label="Tags"
    totalCount={tagTotal}
    items={tagItems}
    onToggle={(v) => toggle("tagKeys", v)}
    storageKey="fotobank:facet:tags"
    searchPlaceholder="search tags…"
  />
  {#if route !== "map"}
    <FacetSection
      label="Places"
      totalCount={placesTotal}
      items={placesItems}
      onToggle={(v) => toggle("hasGps", v)}
      storageKey="fotobank:facet:places"
    />
  {/if}
  <FacetSection
    label="Media Type"
    totalCount={mtTotal}
    items={mediaTypeItems}
    onToggle={(v) => toggle("mediaType", v)}
    storageKey="fotobank:facet:mediatype"
  />
</div>

<style>
  .filter-sidebar {
    padding: 18px 20px;
    border-top: 1px solid var(--border-default);
  }
  .group-header {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--space-3);
    margin-bottom: var(--space-5);
    min-height: 18px;
  }
  /* Horizontal rule running across the full row, behind the label
     and clear button — both labels paint a bg-coloured pill on top
     to interrupt it ("─── index ─────────  clear ↵"). */
  .rule {
    position: absolute;
    left: 0;
    right: 0;
    top: 50%;
    border-top: 1px solid var(--accent-blue);
    pointer-events: none;
    z-index: 0;
  }
  /* "INDEX" sits on the same x=var(--space-4) (8px) guideline as the
     section labels (FacetSection .label) and the row text (FacetList
     .row), so the whole sidebar reads against a single vertical
     gutter instead of three different indents. */
  .title {
    position: relative;
    z-index: 1;
    display: inline-block;
    padding: 0 var(--space-4);
    background: var(--bg-primary);
  }
  .title-display {
    font-family: var(--font-mono);
    font-weight: 600;
    font-size: var(--font-size-2xs);
    color: var(--text-primary);
    text-transform: uppercase;
    letter-spacing: var(--letter-spacing-label);
  }
  .title-a11y {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
  .clear {
    position: relative;
    z-index: 1;
    margin-left: auto;
    padding: 0 0 0 var(--space-3);
    background: var(--bg-primary);
    border: 0;
    color: var(--text-muted);
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    text-transform: lowercase;
    letter-spacing: 0;
    transition: color 100ms;
  }
  .clear:hover { color: var(--accent-blue); }
  .clear-glyph {
    margin-left: 2px;
  }
</style>

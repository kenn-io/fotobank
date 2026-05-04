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
    <span class="label">FILTERS</span>
    {#if !isEmpty(filters)}
      <button type="button" class="clear" onclick={clearAll}>Clear</button>
    {/if}
  </div>

  <FacetSection
    label="Cameras"
    totalCount={cameraTotal}
    items={cameraItems}
    onToggle={(v) => toggle("cameras", v)}
    storageKey="fotobank:facet:cameras"
    searchPlaceholder="Search cameras…"
  />
  <FacetSection
    label="Lenses"
    totalCount={lensTotal}
    items={lensItems}
    onToggle={(v) => toggle("lenses", v)}
    storageKey="fotobank:facet:lenses"
    searchPlaceholder="Search lenses…"
  />
  <FacetSection
    label="Tags"
    totalCount={tagTotal}
    items={tagItems}
    onToggle={(v) => toggle("tagKeys", v)}
    storageKey="fotobank:facet:tags"
    searchPlaceholder="Search tags…"
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
    border-top: 1px solid var(--border);
  }
  .group-header {
    display: flex; align-items: center; justify-content: space-between;
    margin-bottom: var(--space-3);
  }
  .label {
    font-size: var(--text-xs); font-weight: 600;
    color: var(--ink-3);
    text-transform: uppercase;
    letter-spacing: var(--label-track);
  }
  .clear {
    background: transparent; border: 0;
    color: var(--amber);
    font-size: var(--text-xs);
    cursor: pointer;
  }
  .clear:hover { color: var(--amber-deep); }
</style>

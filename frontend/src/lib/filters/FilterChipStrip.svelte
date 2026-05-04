<script lang="ts">
  import { isEmpty, type ActiveFilters } from "./activeFilters";

  let {
    filters,
    tagLabels,
    onChange,
  }: {
    filters: ActiveFilters;
    tagLabels: Record<string, string>;
    onChange: (next: ActiveFilters) => void;
  } = $props();

  type Chip = { kind: string; value: string; display: string };

  const chips = $derived.by((): Chip[] => {
    const out: Chip[] = [];
    for (const v of filters.cameras) out.push({ kind: "camera", value: v, display: v });
    for (const v of filters.lenses) out.push({ kind: "lens", value: v, display: v });
    for (const k of filters.tagKeys) {
      out.push({ kind: "tag", value: k, display: `tag: ${tagLabels[k] ?? k}` });
    }
    if (filters.hasGps === true) out.push({ kind: "has_gps", value: "true", display: "Has GPS" });
    if (filters.hasGps === false) out.push({ kind: "has_gps", value: "false", display: "No GPS" });
    if (filters.mediaType !== null) {
      out.push({ kind: "media_type", value: filters.mediaType,
        display: filters.mediaType === "photo" ? "Photo" : "Video" });
    }
    return out;
  });

  function remove(kind: string, value: string) {
    const f = { ...filters };
    switch (kind) {
      case "camera":     f.cameras = f.cameras.filter((v) => v !== value); break;
      case "lens":       f.lenses = f.lenses.filter((v) => v !== value); break;
      case "tag":        f.tagKeys = f.tagKeys.filter((v) => v !== value); break;
      case "has_gps":    f.hasGps = null; break;
      case "media_type": f.mediaType = null; break;
    }
    onChange(f);
  }

  function clearAll() {
    onChange({ cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null });
  }
</script>

{#if !isEmpty(filters)}
  <div class="strip">
    <span class="leading">Filters:</span>
    {#each chips as c (c.kind + ":" + c.value)}
      <button
        type="button"
        class="chip"
        title="Remove"
        onclick={() => remove(c.kind, c.value)}
      >
        <span class="display">{c.display}</span>
        <span class="chip-x" aria-hidden="true">×</span>
      </button>
    {/each}
    {#if chips.length >= 2}
      <button type="button" class="clear-all" onclick={clearAll}>Clear all</button>
    {/if}
  </div>
{/if}

<style>
  .strip {
    display: flex; flex-wrap: wrap; align-items: center;
    gap: var(--space-2);
    padding: var(--space-3) var(--space-4);
    border-bottom: 1px solid var(--border);
    background: var(--surface);
  }
  .leading {
    font-size: var(--text-xs); color: var(--ink-4);
    text-transform: uppercase;
    letter-spacing: var(--label-track);
  }
  .chip {
    display: inline-flex; align-items: center; gap: var(--space-2);
    height: 24px; padding: 0 8px;
    background: color-mix(in srgb, var(--amber) 14%, transparent);
    color: var(--amber);
    border: 1px solid color-mix(in srgb, var(--amber) 24%, transparent);
    font-size: var(--text-sm); font-weight: 500;
    cursor: pointer;
    transition: background 100ms;
  }
  .chip:hover { background: color-mix(in srgb, var(--amber) 22%, transparent); }
  .chip-x { font-size: 14px; opacity: 0.65; line-height: 1; }
  .clear-all {
    margin-left: auto;
    background: transparent; border: 0;
    color: var(--ink-3);
    font-size: var(--text-sm);
    cursor: pointer;
  }
  .clear-all:hover { color: var(--ink); }
</style>

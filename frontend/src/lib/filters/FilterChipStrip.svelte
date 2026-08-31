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

  type Chip = { kind: string; value: string; display: string; tagLabel?: string };

  const chips = $derived.by((): Chip[] => {
    const out: Chip[] = [];
    for (const v of filters.cameras) out.push({ kind: "camera", value: v, display: v });
    for (const v of filters.lenses) out.push({ kind: "lens", value: v, display: v });
    for (const k of filters.tagKeys) {
      out.push({
        kind: "tag",
        value: k,
        display: `tag: ${tagLabels[k] ?? k}`,
        tagLabel: tagLabels[k] ?? k,
      });
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
    <span class="leading-rule" aria-hidden="true"></span>
    {#each chips as c (c.kind + ":" + c.value)}
      <button
        type="button"
        class="chip"
        title="Remove"
        onclick={() => remove(c.kind, c.value)}
      >
        <span class="bracket" aria-hidden="true">[</span>
        {#if c.kind === "tag"}
          <span class="tag-prefix">tag:</span>
          <span class="display">{c.tagLabel}</span>
        {:else}
          <span class="display">{c.display}</span>
        {/if}
        <span class="chip-x" aria-hidden="true">&#x2715;</span>
        <span class="bracket" aria-hidden="true">]</span>
      </button>
    {/each}
    {#if chips.length >= 2}
      <button type="button" class="clear-all" onclick={clearAll}>Clear all</button>
    {/if}
  </div>
{/if}

<style>
  .strip {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-3) var(--space-4);
    border-bottom: 1px solid var(--border-default);
  }
  /* Leading 2px amber rule replaces the old "Filters:" word —
     a film-strip mark at the left edge of the annotation. */
  .leading-rule {
    flex: 0 0 auto;
    width: 2px;
    align-self: stretch;
    background: var(--accent-blue);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0;
    padding: var(--space-2) var(--space-3);
    background: transparent;
    border: 1px solid transparent;
    color: var(--text-primary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    cursor: pointer;
    transition: border-color 100ms, background 100ms;
  }
  .chip:hover { border-color: var(--fb-accent-deep); }
  .bracket {
    color: var(--accent-blue);
    margin: 0 var(--space-2);
  }
  .tag-prefix {
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    margin-right: var(--space-2);
    text-transform: lowercase;
  }
  .display {
    font-family: var(--font-sans);
    font-variant: small-caps;
    letter-spacing: var(--letter-spacing-label);
    color: var(--text-primary);
  }
  .chip-x {
    color: var(--accent-blue);
    margin-left: var(--space-2);
    font-family: var(--font-mono);
  }
  /* Visual: "── clear all ──" via pseudo-element dashes around the
     literal "Clear all" textContent (kept for test compatibility +
     a11y). text-transform lowercases the rendered word; textContent
     stays "Clear all". */
  .clear-all {
    margin-left: auto;
    padding: 0 var(--space-3);
    background: transparent;
    border: 0;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    text-transform: lowercase;
    cursor: pointer;
    transition: color 100ms;
  }
  .clear-all::before { content: "\2500\2500 "; }
  .clear-all::after  { content: " \2500\2500"; }
  .clear-all:hover { color: var(--accent-blue); }
</style>

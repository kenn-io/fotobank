<!-- frontend/src/lib/components/Sidebar.svelte -->
<script lang="ts">
  import { handleInternalLinkClick } from "../router/router.svelte";
  import type { AppConfigStore } from "../app/appConfig.svelte";
  import FilterSidebar from "../filters/FilterSidebar.svelte";
  import type { ActiveFilters } from "../filters/activeFilters";
  import type { FacetsResponse } from "../filters/facetsStore.svelte";

  // route is the current RouteMatch route name; FilterSidebar is only
  // mounted for the three filter-aware routes. Other routes get the
  // BROWSE/CURATE/MANAGE nav alone — passing dummy filter props for
  // those routes is fine because the FILTERS group is never rendered.
  let {
    active = "",
    appConfig,
    route,
    activeFilters,
    facetsResponse,
    onFiltersChange,
  }: {
    active?: string;
    appConfig: AppConfigStore;
    route: string;
    activeFilters: ActiveFilters;
    facetsResponse: FacetsResponse | null;
    onFiltersChange: (next: ActiveFilters) => void;
  } = $props();

  const showFilters = $derived(
    route === "library" || route === "search" || route === "map",
  );

  const groups = $derived([
    {
      key: "browse",
      label: "BROWSE",
      entries: [
        { id: "library", label: "Library", href: "/library" },
        { id: "sessions", label: "Sessions", href: "/sessions" },
        { id: "map", label: "Map", href: "/map" },
        { id: "hidden", label: "Hidden", href: "/hidden" },
      ],
    },
    {
      key: "curate",
      label: "CURATE",
      entries: [{ id: "albums", label: "Albums", href: "/albums" }],
    },
    {
      key: "manage",
      label: "MANAGE",
      entries: appConfig.sharingEnabled
        ? [{ id: "shares", label: "Shares", href: "/shares" }]
        : [],
    },
  ]);
</script>

<nav>
  {#each groups as group (group.key)}
    {#if group.entries.length > 0}
      <div class="group" data-group={group.key}>
        <div class="group-header">{group.label}</div>
        {#each group.entries as entry (entry.id)}
          <a
            class="entry"
            class:active={active === entry.id}
            href={entry.href}
            onclick={(e) => handleInternalLinkClick(e, entry.href)}
          >{entry.label}</a>
        {/each}
      </div>
    {/if}
  {/each}
</nav>

{#if showFilters}
  <FilterSidebar
    route={route as "library" | "search" | "map"}
    filters={activeFilters}
    response={facetsResponse}
    onChange={onFiltersChange}
  />
{/if}

<style>
  nav {
    display: flex;
    flex-direction: column;
  }
  .group {
    padding: 18px 20px;
  }
  .group + .group {
    border-top: 1px solid var(--border-default);
  }
  .group-header {
    font-size: var(--font-size-2xs);
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: var(--letter-spacing-label);
    margin-bottom: 10px;
  }
  .entry {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 5px 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    text-decoration: none;
    transition: color 100ms;
  }
  .entry:hover { color: var(--text-primary); }
  .entry.active { color: var(--accent-blue); }
  .entry :global(.count) {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: 11px;
    color: var(--fb-text-faint);
  }
  .entry.active :global(.count) { color: var(--fb-accent-deep); }
</style>

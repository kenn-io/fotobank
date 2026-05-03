<!-- frontend/src/lib/components/Sidebar.svelte -->
<script lang="ts">
  import { handleInternalLinkClick } from "../router/router.svelte";
  import type { AppConfigStore } from "../app/appConfig.svelte";

  let {
    active = "",
    appConfig,
  }: { active?: string; appConfig: AppConfigStore } = $props();

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

<style>
  nav {
    display: flex;
    flex-direction: column;
  }
  .group {
    padding: 18px 20px;
  }
  .group + .group {
    border-top: 1px solid var(--border);
  }
  .group-header {
    font-size: var(--text-xs);
    font-weight: 600;
    color: var(--ink-3);
    text-transform: uppercase;
    letter-spacing: var(--label-track);
    margin-bottom: 10px;
  }
  .entry {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 5px 0;
    color: var(--ink-2);
    font-size: var(--text-base);
    text-decoration: none;
    transition: color 100ms;
  }
  .entry:hover { color: var(--ink); }
  .entry.active { color: var(--amber); }
  .entry :global(.count) {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: 11px;
    color: var(--ink-4);
  }
  .entry.active :global(.count) { color: var(--amber-deep); }
</style>

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
  nav { padding: 12px 8px; display: flex; flex-direction: column; gap: 12px; }
  .group { display: flex; flex-direction: column; gap: 2px; }
  .group-header {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.6px;
    color: var(--text-muted);
    padding: 4px 8px 2px;
  }
  .entry {
    display: block;
    padding: 5px 10px;
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    text-decoration: none;
    font-size: 13px;
  }
  .entry:hover { background: var(--bg-elevated); }
  .entry.active { background: var(--accent); color: white; }
</style>

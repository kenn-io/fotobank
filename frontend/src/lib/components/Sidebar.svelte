<!-- frontend/src/lib/components/Sidebar.svelte -->
<script lang="ts">
  import { handleInternalLinkClick } from "../router/router.svelte";

  let { active = "" }: { active?: string } = $props();

  const groups = [
    {
      key: "browse",
      label: "BROWSE",
      entries: [
        { id: "library", label: "Library", href: "/library" },
        { id: "sessions", label: "Sessions", href: "/sessions" },
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
      entries: [{ id: "shares", label: "Shares", href: "/shares" }],
    },
  ];
</script>

<nav>
  {#each groups as group (group.key)}
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
    border-radius: var(--radius);
    color: var(--text-primary);
    text-decoration: none;
    font-size: 13px;
  }
  .entry:hover { background: var(--bg-elevated); }
  .entry.active { background: var(--accent); color: white; }
</style>

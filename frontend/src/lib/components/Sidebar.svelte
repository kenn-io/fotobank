<!-- frontend/src/lib/components/Sidebar.svelte -->
<script lang="ts">
  import { handleInternalLinkClick } from "../router/router.svelte";

  let { active }: { active: string } = $props();

  const items = [
    { group: "BROWSE", entries: [
      { id: "library", label: "Library", href: "/library" },
      { id: "sessions", label: "Sessions", href: "/sessions" },
    ]},
    { group: "", entries: [
      { id: "settings", label: "Settings", href: "/settings" },
    ]},
  ];
</script>

<nav>
  {#each items as section (section.group + section.entries.map(e => e.id).join(','))}
    {#if section.group}
      <div class="group">{section.group}</div>
    {/if}
    {#each section.entries as entry (entry.id)}
      <a
        class="entry"
        class:active={active === entry.id}
        href={entry.href}
        onclick={(e) => handleInternalLinkClick(e, entry.href)}
      >{entry.label}</a>
    {/each}
  {/each}
</nav>

<style>
  nav { padding: 12px 8px; display: flex; flex-direction: column; gap: 2px; }
  .group {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.6px;
    color: var(--text-muted);
    padding: 12px 8px 4px;
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

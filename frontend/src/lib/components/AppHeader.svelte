<!-- frontend/src/lib/components/AppHeader.svelte -->
<script lang="ts">
  let searchEl: HTMLInputElement | null = $state(null);

  $effect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        searchEl?.focus();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });
</script>

<header class="strip">
  <div class="brand">fotobank</div>
  <div class="identity">stub: alice</div>
  <input
    bind:this={searchEl}
    class="search"
    type="search"
    placeholder="Search ⌘K"
    aria-label="Search"
  />
  <button class="account" aria-label="Account menu">⋯</button>
</header>

<style>
  .strip {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 6px 12px;
    border-bottom: 1px solid var(--border);
    background: var(--bg-elevated);
    height: 44px;
    box-shadow: var(--shadow);
  }
  .brand { font-weight: 600; font-size: 14px; }
  .identity { font-size: 12px; color: var(--text-secondary); }
  .search {
    flex: 1;
    max-width: 540px;
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: 14px;
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: 13px;
    margin-left: auto;
  }
  .account {
    background: transparent;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 4px 10px;
    color: var(--text-primary);
    cursor: pointer;
  }
</style>

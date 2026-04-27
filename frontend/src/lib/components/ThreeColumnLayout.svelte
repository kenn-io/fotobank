<!-- frontend/src/lib/components/ThreeColumnLayout.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";

  let { sidebar, main, detail }: {
    sidebar: Snippet;
    main: Snippet;
    detail?: Snippet;
  } = $props();
</script>

<div class="shell">
  <aside class="sidebar">{@render sidebar()}</aside>
  <section class="main">{@render main()}</section>
  {#if detail}
    <aside class="detail">{@render detail()}</aside>
  {/if}
</div>

<style>
  .shell {
    display: grid;
    grid-template-columns: 220px 1fr auto;
    height: 100vh;
    width: 100vw;
  }
  .sidebar {
    background: var(--bg-surface);
    border-right: 1px solid var(--border);
    overflow-y: auto;
  }
  .main {
    overflow: auto;
    background: var(--bg-primary);
  }
  .detail {
    background: var(--bg-surface);
    border-left: 1px solid var(--border);
    overflow-y: auto;
  }
</style>

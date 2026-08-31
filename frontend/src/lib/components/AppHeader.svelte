<!-- frontend/src/lib/components/AppHeader.svelte -->
<script lang="ts">
  import AIStatusDot from "./AIStatusDot.svelte";
  import IdentityChips from "./IdentityChips.svelte";
  import SearchBar from "./SearchBar.svelte";
  import type { Principal } from "../app/appConfig.svelte";

  // Lightroom-pattern header: brand mark + search + identity. Section
  // navigation lives entirely in the sidebar — keeping a second nav
  // surface here only created visual redundancy. This header is the
  // SPA's identity strip and global search affordance, nothing more.
  let {
    principal,
    ready,
    query = "",
    onsearch,
  }: {
    principal: Principal | null;
    ready: boolean;
    // The current /search?q=… value (or "" elsewhere). Forwarded to
    // SearchBar so deep-linked searches, reloads, and back/forward
    // navigation show the same query in the always-visible field as
    // the result list — not an empty/stale input.
    query?: string;
    onsearch: (q: string) => void;
  } = $props();
</script>

<header class="top">
  <span class="brand">fotobank</span>
  <SearchBar {query} onsubmit={onsearch} />
  <div class="top-right">
    <AIStatusDot />
    <IdentityChips {principal} {ready} />
  </div>
</header>

<style>
  header.top {
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: center;
    gap: 24px;
    padding: 0 22px;
    height: 46px;
    border-bottom: 1px solid var(--border-default);
    background: linear-gradient(180deg, #101015 0%, #0c0c11 100%);
    /* Relief — top-edge highlight reads as light grazing a panel that
       sits forward of the page; bottom rim deepens the existing border. */
    box-shadow: var(--fb-shadow-relief);
  }

  .brand {
    font-family: var(--font-mono);
    font-weight: 500;
    font-size: 15px;
    color: var(--text-primary);
    letter-spacing: 0;
    display: inline-flex;
    align-items: baseline;
    gap: 7px;
  }
  .brand::after {
    content: "";
    display: inline-block;
    width: 5px;
    height: 5px;
    background: var(--accent-blue);
    box-shadow: 0 0 9px var(--fb-accent-glow);
    transform: translateY(-1px);
  }

  .top-right {
    display: flex;
    align-items: center;
    gap: 10px;
  }
</style>

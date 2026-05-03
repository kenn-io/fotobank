<!-- frontend/src/lib/components/AppHeader.svelte -->
<script lang="ts">
  import AIStatusDot from "./AIStatusDot.svelte";
  import IdentityChips from "./IdentityChips.svelte";
  import SearchBar from "./SearchBar.svelte";
  import { handleInternalLinkClick, router } from "../router/router.svelte";
  import type { Principal } from "../app/appConfig.svelte";

  // Props are minimal: identity for the chips, ready flag (renders
  // chips only after /me has resolved), and an `onsearch` callback
  // the host wires to its router. Keeping the SearchBar's
  // navigation contract on the host means this header doesn't need
  // to import the router for that path; it still imports `router`
  // for the active-tab highlight, which reads `router.current`.
  let {
    principal,
    ready,
    onsearch,
  }: {
    principal: Principal | null;
    ready: boolean;
    onsearch: (q: string) => void;
  } = $props();

  // Map the matched route to a tab key. We collapse media-detail and
  // anything else that "lives in the library context" onto the
  // Library tab so a deep-link to /media/<id> still highlights it.
  // Routes that have no top-tab home (sessions, settings, search,
  // shares) fall through to the empty string and no tab is active —
  // matches the prior AppHeader's behavior of leaving the strip
  // unhighlighted on those screens.
  const activeTab = $derived.by((): string => {
    const r = router.current;
    if (r.route === "library" || r.route === "media") return "library";
    if (r.route === "map") return "map";
    if (r.route === "albums" || r.route === "albums.detail") return "albums";
    if (r.route === "hidden") return "hidden";
    return "";
  });
</script>

<header class="top">
  <span class="brand">fotobank</span>
  <nav class="tabs">
    <a
      href="/library"
      class:active={activeTab === "library"}
      onclick={(e) => handleInternalLinkClick(e, "/library")}
    >Library</a>
    <a
      href="/map"
      class:active={activeTab === "map"}
      onclick={(e) => handleInternalLinkClick(e, "/map")}
    >Map</a>
    <a
      href="/albums"
      class:active={activeTab === "albums"}
      onclick={(e) => handleInternalLinkClick(e, "/albums")}
    >Albums</a>
    <a
      href="/hidden"
      class:active={activeTab === "hidden"}
      onclick={(e) => handleInternalLinkClick(e, "/hidden")}
    >Hidden</a>
  </nav>
  <SearchBar onsubmit={onsearch} />
  <div class="top-right">
    <AIStatusDot />
    <IdentityChips {principal} {ready} />
  </div>
</header>

<style>
  header.top {
    display: grid;
    grid-template-columns: auto auto 1fr auto;
    align-items: center;
    gap: 24px;
    padding: 0 22px;
    height: 46px;
    border-bottom: 1px solid var(--border);
    background: linear-gradient(180deg, #101015 0%, #0c0c11 100%);
  }

  .brand {
    font-family: var(--font-mono);
    font-weight: 500;
    font-size: 15px;
    color: var(--ink);
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
    background: var(--amber);
    box-shadow: 0 0 9px var(--amber-glow);
    transform: translateY(-1px);
  }

  nav.tabs {
    display: flex;
    gap: 0;
    height: 100%;
  }
  nav.tabs a {
    position: relative;
    display: inline-flex;
    align-items: center;
    padding: 0 16px;
    color: var(--ink-3);
    font-size: 11px;
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: var(--label-track);
    text-decoration: none;
    transition: color 120ms;
  }
  nav.tabs a:hover {
    color: var(--ink-2);
  }
  nav.tabs a.active {
    color: var(--ink);
  }
  nav.tabs a.active::after {
    content: "";
    position: absolute;
    left: 16px;
    right: 16px;
    bottom: -1px;
    height: 1px;
    background: var(--amber);
    box-shadow: 0 0 8px var(--amber-glow);
  }

  .top-right {
    display: flex;
    align-items: center;
    gap: 10px;
  }
</style>

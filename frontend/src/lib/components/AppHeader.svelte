<!-- frontend/src/lib/components/AppHeader.svelte -->
<script lang="ts">
  import AIStatusDot from "./AIStatusDot.svelte";
  import { router } from "../router/router.svelte";
  import type { Theme } from "../theme/themeStore.svelte";

  // Identity, search, and theme controls live here. Identity comes from
  // /api/v1/me via AppConfigStore (App.svelte threads it through). Theme
  // is the same — App.svelte owns the ThemeStore and forwards the
  // current value plus a setter, so this component stays free of
  // module-singleton imports and tests can supply minimal stubs.
  let {
    hub,
    handle,
    theme,
    onSetTheme,
  }: {
    hub?: string | undefined;
    handle?: string | undefined;
    theme?: Theme | undefined;
    onSetTheme?: ((next: Theme) => void) | undefined;
  } = $props();

  let searchEl: HTMLInputElement | null = $state(null);
  let value = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;
  const DEBOUNCE_MS = 300;

  // ⌘K (and Ctrl+K) focuses the search box from anywhere in the app.
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

  // When the route changes externally (sidebar nav, back/forward, deep
  // link), mirror the q from the URL into the input. Without this the
  // input drifts away from the URL and a /search ↔ /library round-trip
  // shows stale text. router.current is a `$state` so the effect
  // re-runs whenever the route shape changes. We compute the desired
  // string with $derived so the effect's dependency is the *URL-sourced
  // value*, not `value` itself — otherwise typing into the input
  // (which mutates `value`) would re-fire the effect and reset the
  // input back to the URL on every keystroke.
  const urlQuery = $derived.by((): string => {
    const r = router.current;
    if (r.route === "search") return r.q ?? "";
    return "";
  });
  $effect(() => {
    value = urlQuery;
  });

  // Clear the pending timer on unmount so a debounced commit can't
  // fire after the component has been torn down (would reach into a
  // detached DOM).
  $effect(() => {
    return () => {
      if (timer !== undefined) {
        clearTimeout(timer);
        timer = undefined;
      }
    };
  });

  // Cancel any pending debounce when the route changes externally. If
  // the user types "do" on /library and navigates away (sidebar click,
  // back/forward) before the debounce fires, the stale timer would
  // otherwise navigate back to /search?q=do and stomp the user's chosen
  // route. The effect runs whenever router.current changes, including
  // immediately after mount, which is harmless because timer is
  // undefined at that point.
  $effect(() => {
    const _ = router.current;
    void _;
    if (timer !== undefined) {
      clearTimeout(timer);
      timer = undefined;
    }
  });

  function onInput(): void {
    // `bind:value` already mirrors the DOM value into `value`; this
    // handler exists purely for the debounce side effect.
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(() => commit(), DEBOUNCE_MS);
  }

  function onKeyDown(ev: KeyboardEvent): void {
    if (ev.key === "Enter") {
      ev.preventDefault();
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
      commit({ pushHistory: true });
    }
  }

  // commit syncs `value` to the URL by navigating to /search. The
  // contract:
  //  - On /search with no Enter: replace the current history entry so
  //    typing doesn't fill the back stack.
  //  - On Enter (anywhere): push a new entry so back returns to the
  //    previous page (or to the prior query).
  //  - From any other route: push a new entry so back returns to that
  //    page.
  function commit({ pushHistory = false }: { pushHistory?: boolean } = {}): void {
    const params = new URLSearchParams(window.location.search);
    if (value !== "") {
      params.set("q", value);
    } else {
      params.delete("q");
    }
    const qs = params.toString();
    const target = qs.length > 0 ? `/search?${qs}` : "/search";
    if (window.location.pathname === "/search" && !pushHistory) {
      router.navigate(target, { replace: true });
    } else {
      router.navigate(target);
    }
  }

  // Account dropdown (theme switcher today; will gain more entries as
  // the settings surface grows). Click-outside / Escape both close it.
  // Listener is conditional on menuOpen so we only pay the document
  // listener cost while the menu is actually visible.
  let menuOpen = $state(false);
  let menuEl: HTMLDivElement | null = $state(null);

  $effect(() => {
    if (!menuOpen) return;
    function onDocPointer(e: PointerEvent): void {
      if (menuEl === null) return;
      if (e.target instanceof Node && menuEl.contains(e.target)) return;
      menuOpen = false;
    }
    function onDocKey(e: KeyboardEvent): void {
      if (e.key === "Escape") menuOpen = false;
    }
    document.addEventListener("pointerdown", onDocPointer);
    document.addEventListener("keydown", onDocKey);
    return () => {
      document.removeEventListener("pointerdown", onDocPointer);
      document.removeEventListener("keydown", onDocKey);
    };
  });

  function chooseTheme(next: Theme): void {
    onSetTheme?.(next);
    menuOpen = false;
  }

  // Identity display: `{hub}: {handle}` when /me has resolved, em-dash
  // placeholders before that. We never fall through to a placeholder
  // user_id like "alice" — a missing principal is a real state worth
  // signaling, not faked.
  const identityHub = $derived(hub ?? "—");
  const identityHandle = $derived(handle ?? "—");
</script>

<header class="strip">
  <div class="brand">fotobank</div>
  <div class="identity" data-testid="app-identity">{identityHub}: {identityHandle}</div>
  <input
    bind:this={searchEl}
    bind:value
    class="search"
    type="search"
    placeholder="Search ⌘K"
    aria-label="Search"
    oninput={onInput}
    onkeydown={onKeyDown}
  />
  <AIStatusDot />
  <div class="account-menu" bind:this={menuEl}>
    <button
      class="account"
      aria-label="Account menu"
      aria-expanded={menuOpen}
      aria-haspopup="menu"
      onclick={() => (menuOpen = !menuOpen)}
    >⋯</button>
    {#if menuOpen}
      <div class="account-dropdown" role="menu" data-testid="account-dropdown">
        <div class="dropdown-section-label">Theme</div>
        <button
          type="button"
          role="menuitemradio"
          aria-checked={theme === "system"}
          class="dropdown-item"
          class:active={theme === "system"}
          onclick={() => chooseTheme("system")}
        >System</button>
        <button
          type="button"
          role="menuitemradio"
          aria-checked={theme === "light"}
          class="dropdown-item"
          class:active={theme === "light"}
          onclick={() => chooseTheme("light")}
        >Light</button>
        <button
          type="button"
          role="menuitemradio"
          aria-checked={theme === "dark"}
          class="dropdown-item"
          class:active={theme === "dark"}
          onclick={() => chooseTheme("dark")}
        >Dark</button>
      </div>
    {/if}
  </div>
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
    box-shadow: var(--shadow-sm);
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
  .account-menu {
    position: relative;
  }
  .account {
    background: transparent;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 4px 10px;
    color: var(--text-primary);
    cursor: pointer;
  }
  .account-dropdown {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    min-width: 160px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-sm);
    padding: 4px;
    z-index: 100;
  }
  .dropdown-section-label {
    font-size: 11px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    padding: 6px 8px 2px;
  }
  .dropdown-item {
    display: block;
    width: 100%;
    text-align: left;
    background: transparent;
    border: 0;
    padding: 6px 8px;
    color: var(--text-primary);
    font-size: 13px;
    border-radius: 4px;
    cursor: pointer;
  }
  .dropdown-item:hover {
    background: var(--bg-surface);
  }
  .dropdown-item.active {
    color: var(--accent);
    font-weight: 600;
  }
</style>

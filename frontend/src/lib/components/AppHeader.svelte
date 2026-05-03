<!-- frontend/src/lib/components/AppHeader.svelte -->
<script lang="ts">
  import AIStatusDot from "./AIStatusDot.svelte";
  import { router } from "../router/router.svelte";

  // Identity and search live here. Identity comes from /api/v1/me via
  // AppConfigStore (App.svelte threads it through), keeping this
  // component free of module-singleton imports so tests can supply
  // minimal stubs.
  let {
    hub,
    handle,
  }: {
    hub?: string | undefined;
    handle?: string | undefined;
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
</header>

<style>
  .strip {
    display: flex;
    align-items: center;
    gap: var(--space-5);
    padding: var(--space-3) var(--space-5);
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
    height: 44px;
  }
  .brand { font-weight: 600; font-size: var(--text-md); }
  .identity { font-size: var(--text-sm); color: var(--ink-2); }
  .search {
    flex: 1;
    max-width: 540px;
    height: 28px;
    padding: 0 var(--space-4);
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--ink);
    font-size: var(--text-base);
    margin-left: auto;
  }
</style>

<!-- frontend/src/lib/components/SearchBar.svelte -->
<script lang="ts">
  // Always-visible search input that lives in the AppHeader's middle
  // column. ⌘K (Cmd on macOS, Ctrl elsewhere) focuses the input from
  // anywhere in the app; Enter submits the trimmed query via
  // `onsubmit`. Empty / whitespace-only queries are skipped — the
  // host doesn't need to guard against them.
  //
  // The component is intentionally narrow: no debounce, no URL
  // writes, no router import. The host (App.svelte) owns navigation,
  // so this stays a pure leaf and is trivial to test.
  let {
    query = "",
    onsubmit,
  }: {
    query?: string;
    onsubmit: (q: string) => void;
  } = $props();

  let inputEl: HTMLInputElement | null = $state(null);
  // Local input state. Initialized empty and seeded from `query` via
  // an effect so the literal-on-init read of `query` (which would
  // capture only the first frame) doesn't show up as a Svelte 5
  // state_referenced_locally warning.
  let value = $state("");
  // Mirrors `:focus-within` for the kbd-hint hide. Kept in addition
  // to the CSS rule because jsdom doesn't compute :focus-within in
  // unit tests; toggling a class lets the test scope assertions to
  // the focused state without hand-rolling computed-style logic.
  let focused = $state(false);

  // Mirror an externally-supplied `query` prop into the input on
  // change so the host can seed the value (e.g. from ?q=) without
  // racing the user's typing. The effect keys on `query` only —
  // typing into the input mutates `value` independently.
  $effect(() => {
    value = query;
  });

  // ⌘K (and Ctrl+K) focuses the input. preventDefault stops the
  // browser's "find" affordance from racing — without it, Chrome
  // briefly opens its own search bar before our focus lands.
  $effect(() => {
    function onKey(e: KeyboardEvent): void {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        inputEl?.focus();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  function onKeyDown(ev: KeyboardEvent): void {
    if (ev.key !== "Enter") return;
    ev.preventDefault();
    const trimmed = value.trim();
    if (trimmed === "") return;
    onsubmit(trimmed);
  }
</script>

<div class="search-bar" class:focused>
  <svg
    class="search-icon"
    width="13"
    height="13"
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    stroke-width="1.5"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <circle cx="6.5" cy="6.5" r="5"></circle>
    <path d="M14 14l-3.5-3.5"></path>
  </svg>
  <input
    bind:this={inputEl}
    bind:value
    type="search"
    aria-label="Search"
    placeholder="Search photos, cameras, places, dates…"
    data-testid="search-input"
    onkeydown={onKeyDown}
    onfocus={() => (focused = true)}
    onblur={() => (focused = false)}
  />
  <kbd>⌘K</kbd>
</div>

<style>
  .search-bar {
    position: relative;
    width: 100%;
    max-width: 520px;
    justify-self: center;
  }
  .search-bar input {
    width: 100%;
    height: 28px;
    padding: 0 56px 0 32px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--ink);
    font-family: var(--font-ui);
    font-size: 12.5px;
    letter-spacing: -0.005em;
    outline: none;
    transition: border-color 140ms, background 140ms, box-shadow 140ms;
  }
  .search-bar input::placeholder {
    color: var(--ink-3);
  }
  .search-bar input::-webkit-search-cancel-button {
    display: none;
  }
  .search-bar input:hover {
    border-color: var(--border-2);
  }
  .search-bar input:focus {
    border-color: var(--amber);
    background: var(--surface-2);
    box-shadow: 0 0 0 1px var(--amber-glow), 0 0 14px rgba(232, 164, 75, 0.12);
  }
  .search-bar .search-icon {
    position: absolute;
    left: 11px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--ink-3);
    pointer-events: none;
    transition: color 140ms;
  }
  .search-bar:focus-within .search-icon {
    color: var(--amber);
  }
  .search-bar kbd {
    position: absolute;
    right: 8px;
    top: 50%;
    transform: translateY(-50%);
    font-family: var(--font-mono);
    font-size: 10px;
    padding: 1px 5px;
    color: var(--ink-3);
    border: 1px solid var(--border-2);
    background: var(--bg);
    pointer-events: none;
    transition: opacity 120ms;
    opacity: 1;
  }
  .search-bar:focus-within kbd,
  .search-bar.focused kbd {
    opacity: 0;
  }
</style>

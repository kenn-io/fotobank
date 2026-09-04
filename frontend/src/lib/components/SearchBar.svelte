<!-- frontend/src/lib/components/SearchBar.svelte -->
<script lang="ts">
  import { SearchInput } from "@kenn-io/kit-ui";

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

  let inputEl: HTMLInputElement | undefined = $state();
  // Local input state. Initialized empty and seeded from `query` via
  // an effect so the literal-on-init read of `query` (which would
  // capture only the first frame) doesn't show up as a Svelte 5
  // state_referenced_locally warning.
  let value = $state("");
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

<div class="search-bar">
  <SearchInput
    bind:inputEl
    bind:value
    ariaLabel="Search"
    placeholder="Search photos, cameras, places, dates…"
    keys={["⌘", "K"]}
    onkeydown={onKeyDown}
    block
  />
</div>

<style>
  .search-bar {
    position: relative;
    width: 100%;
    max-width: 520px;
    justify-self: center;
  }
  .search-bar:focus-within :global(.kit-kbd-badge) {
    opacity: 0;
  }
</style>

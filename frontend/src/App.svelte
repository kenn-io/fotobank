<!-- frontend/src/App.svelte -->
<script lang="ts">
  import { onDestroy } from "svelte";
  import ThreeColumnLayout from "./lib/components/ThreeColumnLayout.svelte";
  import AppHeader from "./lib/components/AppHeader.svelte";
  import Sidebar from "./lib/components/Sidebar.svelte";
  import Library from "./routes/Library.svelte";
  import { ThemeStore } from "./lib/theme/themeStore.svelte";
  import { EventsStore } from "./lib/events/eventsStore.svelte";
  import { selection } from "./lib/selection/selectionStore.svelte";
  import { api } from "./lib/api/client";

  const themeStore = new ThemeStore(api);
  themeStore.load();
  const events = new EventsStore();
  events.connect();
  // HMR remounts the root component; without an explicit teardown the
  // EventSource accumulates duplicate connections each reload.
  onDestroy(() => events.disconnect());

  let route = $state(window.location.pathname || "/library");

  $effect(() => {
    const onPop = () => (route = window.location.pathname || "/library");
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  });

  $effect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      // No-op when nothing is selected so we don't shadow other Esc
      // handlers (modals, popovers) that future tasks will introduce.
      if (selection.ids.size === 0) return;
      // Don't steal Escape from text inputs — Esc there usually means
      // "dismiss the dropdown / cancel the edit", not "clear selection".
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
      selection.clear();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  function activeId(path: string): string {
    if (path.startsWith("/sessions")) return "sessions";
    if (path.startsWith("/settings")) return "settings";
    return "library";
  }
</script>

<AppHeader />
<ThreeColumnLayout>
  {#snippet sidebar()}
    <Sidebar active={activeId(route)} />
  {/snippet}
  {#snippet main()}
    {#if route.startsWith("/settings")}
      <div style="padding:20px">Settings (placeholder; theme = {themeStore.theme})</div>
    {:else if route.startsWith("/sessions")}
      <div style="padding:20px">Sessions route — Task 27</div>
    {:else}
      <Library />
    {/if}
  {/snippet}
</ThreeColumnLayout>

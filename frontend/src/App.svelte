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
      if (e.key === "Escape") selection.clear();
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

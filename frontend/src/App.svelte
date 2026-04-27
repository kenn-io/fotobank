<!-- frontend/src/App.svelte -->
<script lang="ts">
  import ThreeColumnLayout from "./lib/components/ThreeColumnLayout.svelte";
  import AppHeader from "./lib/components/AppHeader.svelte";
  import Sidebar from "./lib/components/Sidebar.svelte";
  import { ThemeStore } from "./lib/theme/themeStore.svelte";
  import { api } from "./lib/api/client";

  const themeStore = new ThemeStore(api);
  themeStore.load();

  let route = $state(window.location.pathname || "/library");

  $effect(() => {
    const onPop = () => (route = window.location.pathname || "/library");
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
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
      <div style="padding:20px">Library route — Task 23</div>
    {/if}
  {/snippet}
</ThreeColumnLayout>

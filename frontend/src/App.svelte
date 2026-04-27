<!-- frontend/src/App.svelte -->
<script lang="ts">
  import { onDestroy } from "svelte";
  import ThreeColumnLayout from "./lib/components/ThreeColumnLayout.svelte";
  import AppHeader from "./lib/components/AppHeader.svelte";
  import ActionBar from "./lib/components/ActionBar.svelte";
  import Sidebar from "./lib/components/Sidebar.svelte";
  import Library from "./routes/Library.svelte";
  import Sessions from "./routes/Sessions.svelte";
  import MediaDetail from "./routes/MediaDetail.svelte";
  import NotFound from "./routes/NotFound.svelte";
  import { ThemeStore } from "./lib/theme/themeStore.svelte";
  import { EventsStore } from "./lib/events/eventsStore.svelte";
  import { selection } from "./lib/selection/selectionStore.svelte";
  import { router, type RouteMatch } from "./lib/router/router.svelte";
  import { isEditableTarget } from "./lib/dom/editable";
  import { api } from "./lib/api/client";

  const themeStore = new ThemeStore(api);
  themeStore.load();
  const events = new EventsStore();
  events.connect();
  // HMR remounts the root component; without an explicit teardown the
  // EventSource accumulates duplicate connections each reload.
  onDestroy(() => events.disconnect());

  $effect(() => {
    const onPop = () => router.syncFromLocation();
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
      if (isEditableTarget(e.target)) return;
      selection.clear();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  function activeId(route: RouteMatch): string {
    if (route.route === "sessions") return "sessions";
    if (route.route === "settings") return "settings";
    return "library";
  }
</script>

<AppHeader />
<ActionBar {selection} />
<ThreeColumnLayout>
  {#snippet sidebar()}
    <Sidebar active={activeId(router.current)} />
  {/snippet}
  {#snippet main()}
    {#if router.current.route === "library"}
      <Library />
    {:else if router.current.route === "sessions"}
      <Sessions />
    {:else if router.current.route === "media"}
      <MediaDetail id={router.current.id} />
    {:else if router.current.route === "settings"}
      <div style="padding:20px">Settings (placeholder; theme = {themeStore.theme})</div>
    {:else}
      <NotFound />
    {/if}
  {/snippet}
</ThreeColumnLayout>

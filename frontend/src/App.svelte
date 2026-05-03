<!-- frontend/src/App.svelte -->
<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import ThreeColumnLayout from "./lib/components/ThreeColumnLayout.svelte";
  import AppHeader from "./lib/components/AppHeader.svelte";
  import Sidebar from "./lib/components/Sidebar.svelte";
  import Library from "./routes/Library.svelte";
  import Sessions from "./routes/Sessions.svelte";
  import MediaDetail from "./routes/MediaDetail.svelte";
  import AlbumsIndex from "./routes/AlbumsIndex.svelte";
  import AlbumDetail from "./routes/AlbumDetail.svelte";
  import SharesPage from "./routes/SharesPage.svelte";
  import HiddenLibrary from "./routes/HiddenLibrary.svelte";
  import SettingsAI from "./routes/SettingsAI.svelte";
  import Search from "./routes/Search.svelte";
  import Map from "./routes/Map.svelte";
  import NotFound from "./routes/NotFound.svelte";
  import HiddenLockStrip from "./lib/components/HiddenLockStrip.svelte";
  import ToastStack from "./lib/components/ToastStack.svelte";
  import { EventsStore } from "./lib/events/eventsStore.svelte";
  import { MediaStore } from "./lib/media/mediaStore.svelte";
  import { AlbumsStore } from "./lib/albums/albumsStore.svelte";
  import { SharesStore } from "./lib/shares/sharesStore.svelte";
  import { HiddenStore } from "./lib/hidden/hiddenStore.svelte";
  import { GeoStore } from "./lib/map/geoStore.svelte";
  import { ToastStore } from "./lib/toasts/toastStore.svelte";
  import { selection } from "./lib/selection/selectionStore.svelte";
  import { router, type RouteMatch } from "./lib/router/router.svelte";
  import { isEditableTarget } from "./lib/dom/editable";
  import { modalStack } from "./lib/lightbox/modalStack.svelte";
  import { api } from "./lib/api/client";
  import { aiHealthStore } from "./lib/ai/health.svelte";
  import { AppConfigStore } from "./lib/app/appConfig.svelte";
  import { shouldRedirectSharesToHome } from "./lib/app/routeGuards";

  const events = new EventsStore();
  events.connect();
  // HMR remounts the root component; without an explicit teardown the
  // EventSource accumulates duplicate connections each reload.
  onDestroy(() => events.disconnect());

  const mediaStore = new MediaStore(api);
  mediaStore.loadInitial();
  const albumsStore = new AlbumsStore(api);
  const sharesStore = new SharesStore(api);
  const hiddenStore = new HiddenStore(api);
  hiddenStore.refresh();
  const geoStore = new GeoStore(api);
  const toastStore = new ToastStore();
  const appConfig = new AppConfigStore(api);
  // Initial health snapshot — runs once on mount.
  void aiHealthStore.refresh();

  onMount(() => {
    void appConfig.load();
  });

  // Route guard: when sharing UI is disabled, the /shares route must
  // not render. C1 makes appConfig.ready=true on both /me success and
  // failure (treat-failure-as-disabled), so this fires predictably even
  // if /me errored. replace:true keeps the user from browser-backing
  // into /shares after the redirect. Predicate lives in routeGuards.ts
  // so it can be unit-tested without rendering the full App tree.
  $effect(() => {
    if (shouldRedirectSharesToHome(router.current, appConfig)) {
      router.navigate("/", { replace: true });
    }
  });

  $effect(() => {
    const ev = events.lastEvent;
    if (!ev) return;
    if (
      ev.type === "ai.tag.completed" ||
      ev.type === "ai.caption.completed" ||
      ev.type === "ai.health.changed"
    ) {
      void aiHealthStore.refresh();
    }
  });

  $effect(() => {
    const onPop = () => router.syncFromLocation();
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  });

  $effect(() => {
    const handler = () => {
      if (hiddenStore.unlocked) {
        hiddenStore.lock({ keepalive: true });
      }
    };
    document.addEventListener("visibilitychange", handler);
    window.addEventListener("pagehide", handler);
    return () => {
      document.removeEventListener("visibilitychange", handler);
      window.removeEventListener("pagehide", handler);
    };
  });

  $effect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      // Topmost modal first. dispatchEscape returns true if a stack
      // entry handled the event; we then preventDefault/stopPropagation
      // so selection-clear and any browser default don't fire.
      if (modalStack.dispatchEscape()) {
        e.preventDefault();
        e.stopPropagation();
        return;
      }
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
    if (route.route === "settings" || route.route === "settings.ai") return "settings";
    if (route.route === "albums" || route.route === "albums.detail") return "albums";
    if (route.route === "shares") return "shares";
    if (route.route === "hidden") return "hidden";
    if (route.route === "map") return "map";
    // search has no sidebar entry of its own; the toolbar's search box
    // launches it and the user navigates back via the sidebar links.
    // Returning "" keeps the existing sections from looking active
    // while a search is on screen.
    if (route.route === "search") return "";
    // notfound returns "" so the sidebar highlights nothing — landing
    // on a 404 shouldn't make Library look like the active section.
    if (route.route === "notfound") return "";
    // library and media: media-detail belongs in the library context,
    // so the sidebar keeps Library highlighted while a photo is open.
    return "library";
  }
</script>

<AppHeader
  hub={appConfig.principal?.hub}
  handle={appConfig.principal?.handle}
/>
{#if hiddenStore.unlocked}
  <HiddenLockStrip {hiddenStore} />
{/if}
<ThreeColumnLayout>
  {#snippet sidebar()}
    <Sidebar active={activeId(router.current)} {appConfig} />
  {/snippet}
  {#snippet main()}
    {#if router.current.route === "library"}
      <Library {mediaStore} {albumsStore} {hiddenStore} {toastStore} {appConfig} />
    {:else if router.current.route === "sessions"}
      <Sessions {mediaStore} {albumsStore} {hiddenStore} {toastStore} {appConfig} />
    {:else if router.current.route === "media"}
      <MediaDetail
        id={router.current.id}
        from={router.current.from}
        {mediaStore}
        {albumsStore}
        {hiddenStore}
        {toastStore}
        {appConfig}
      />
    {:else if router.current.route === "albums"}
      <AlbumsIndex {albumsStore} />
    {:else if router.current.route === "albums.detail"}
      <AlbumDetail id={router.current.id} {mediaStore} {albumsStore} {hiddenStore} {toastStore} {appConfig} />
    {:else if router.current.route === "shares"}
      <SharesPage {sharesStore} {appConfig} />
    {:else if router.current.route === "hidden"}
      <HiddenLibrary {hiddenStore} {albumsStore} {toastStore} {appConfig} />
    {:else if router.current.route === "settings"}
      <div style="padding:20px">Settings (placeholder)</div>
    {:else if router.current.route === "settings.ai"}
      <SettingsAI />
    {:else if router.current.route === "search"}
      <Search {events} />
    {:else if router.current.route === "map"}
      <Map
        z={router.current.z}
        c={router.current.c}
        focus={router.current.focus}
        tab={router.current.tab}
        {geoStore}
        {mediaStore}
        {hiddenStore}
        {toastStore}
      />
    {:else}
      <NotFound />
    {/if}
  {/snippet}
</ThreeColumnLayout>
<ToastStack {toastStore} />

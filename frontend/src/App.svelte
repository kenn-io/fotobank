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
  import Settings from "./routes/Settings.svelte";
  import AdminSettingsAI from "./routes/AdminSettingsAI.svelte";
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
  import { FacetsStore } from "./lib/filters/facetsStore.svelte";
  import {
    fromRoute,
    withFilters,
    type ActiveFilters,
  } from "./lib/filters/activeFilters";

  const events = new EventsStore();
  events.connect();
  // HMR remounts the root component; without an explicit teardown the
  // EventSource accumulates duplicate connections each reload.
  onDestroy(() => events.disconnect());

  // Boot-time hydration: seed the mediaStore's filter set from the
  // route URL before firing the first fetch. fromRoute returns an
  // empty filter set on non-filter routes, so this is a no-op for
  // /albums or /sessions; on /library?camera=Sony URLs it primes the
  // filter set so the boot fetch lands filtered instead of relying on
  // Library's mount-time effect to invalidate a wasted unfiltered
  // request.
  const mediaStore = new MediaStore(api);
  mediaStore.setFilters(fromRoute(router.current));
  mediaStore.loadInitial();
  const albumsStore = new AlbumsStore(api);
  const sharesStore = new SharesStore(api);
  const hiddenStore = new HiddenStore(api);
  hiddenStore.refresh();
  const geoStore = new GeoStore(api);
  const toastStore = new ToastStore();
  const appConfig = new AppConfigStore(api);
  // FacetsStore is the single source of truth for the FILTERS sidebar
  // group across /library, /search, /map. App.svelte owns it because
  // the response is route-scoped (each route caches its own facet
  // counts) but the writer (URL → ActiveFilters → router.navigate)
  // also lives here, alongside the other route-level controllers.
  const facetsStore = new FacetsStore(api);
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
    if (
      appConfig.ready &&
      router.current.route === "admin.settings.ai" &&
      !appConfig.adminSettingsEnabled
    ) {
      router.navigate("/settings/ai", { replace: true });
    }
  });

  // activeFilters is the route-derived snapshot of the four
  // multi-value facet groups + has_gps + media_type. fromRoute is
  // pure and returns a stable empty filter set for non-filter
  // routes, so threading this prop down for /albums or /sessions
  // is harmless — the Sidebar conditions on `route` and the
  // FILTERS group never renders.
  const activeFilters = $derived(fromRoute(router.current));

  // tagLabels: tag_key → display label, sourced from the latest
  // facets response. FilterChipStrip uses this to render
  // "tag: Dog" instead of "tag: dog". Falls back to the key in the
  // chip itself when the response hasn't loaded yet — the URL has
  // tag_keys, but the human label only exists in the facets payload.
  const tagLabels = $derived.by((): Record<string, string> => {
    const out: Record<string, string> = {};
    for (const t of facetsStore.response?.tags ?? []) out[t.key] = t.label;
    return out;
  });

  // Refetch facets whenever the route or the filter selection
  // changes. The store internally debounces (100ms) and caches by
  // (route, filterKey, scopeKey) so back-to-back navigations or rapid
  // toggle clicks coalesce into a single GET. Other routes leave the
  // store untouched — its last response stays cached for when the
  // user navigates back.
  //
  // /search alone receives the search-scope payload (q + typed tag
  // labels + date range + location + include_hidden). Without it the
  // /search facet counts would surface alternatives drawn from the
  // entire library, ignoring the user's typed query and chip strip
  // (roborev finding 17964 #2). /library and /map don't carry that
  // surface, so their fetch calls stay bare and the store's cache
  // stays simple.
  $effect(() => {
    const r = router.current;
    if (r.route === "library" || r.route === "map") {
      void facetsStore.fetch(r.route, activeFilters);
    } else if (r.route === "search") {
      void facetsStore.fetch("search", activeFilters, {
        ...(r.q !== undefined ? { q: r.q } : {}),
        ...(r.date_after !== undefined ? { dateAfter: r.date_after } : {}),
        ...(r.date_before !== undefined ? { dateBefore: r.date_before } : {}),
        ...(r.tag !== undefined ? { tagLabels: r.tag } : {}),
        ...(r.location !== undefined ? { location: r.location } : {}),
        ...(r.include_hidden === true ? { includeHidden: true } : {}),
      });
    }
  });

  // onFiltersChange is the URL writer for the FILTERS sidebar +
  // (later) the chip strip. It rewrites only the filter param
  // keys (camera/lens/facet_tag/has_gps/media_type) — every
  // other query param (q, sort, date_after/before, location,
  // include_hidden, z, c, focus, tab) is preserved, so toggling
  // a camera on /search?q=… doesn't drop the user's query.
  function onFiltersChange(next: ActiveFilters) {
    const sp = withFilters(
      new URLSearchParams(window.location.search),
      next,
    );
    const qs = sp.toString();
    router.navigate(
      `${window.location.pathname}${qs !== "" ? `?${qs}` : ""}`,
    );
  }

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

  // SearchBar (in AppHeader) calls this with the trimmed query when
  // the user presses Enter. Empty inputs are skipped at the SearchBar
  // layer so this only fires with a real string; we still guard
  // defensively because the prop is publicly callable.
  //
  // When already on /search, preserve every other URL param (sort, tag,
  // date_after/before, location, media_type, include_hidden) so changing
  // the query doesn't silently drop the user's filter selection. Off
  // the search route, navigate to a fresh /search with only ?q= set.
  function onSearchSubmit(q: string): void {
    if (q === "") return;
    if (router.current.route === "search") {
      const sp = new URLSearchParams(window.location.search);
      sp.set("q", q);
      router.navigate(`/search?${sp.toString()}`);
      return;
    }
    router.navigate(`/search?q=${encodeURIComponent(q)}`);
  }

  // currentSearchQuery feeds AppHeader → SearchBar so the always-
  // visible input mirrors the current /search?q= value. Empty string
  // off the search route, or when /search has no q param, so the
  // input renders blank rather than retaining the prior page's query.
  const currentSearchQuery = $derived(
    router.current.route === "search" ? (router.current.q ?? "") : "",
  );

  function activeId(route: RouteMatch): string {
    if (route.route === "sessions") return "sessions";
    if (route.route === "settings" || route.route === "settings.ai" || route.route === "admin.settings.ai") return "settings";
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
  principal={appConfig.principal}
  ready={appConfig.ready}
  query={currentSearchQuery}
  onsearch={onSearchSubmit}
/>
{#if hiddenStore.unlocked}
  <HiddenLockStrip {hiddenStore} />
{/if}
<ThreeColumnLayout routeKey={router.current.route}>
  {#snippet sidebar()}
    <Sidebar
      active={activeId(router.current)}
      {appConfig}
      route={router.current.route}
      {activeFilters}
      facetsResponse={facetsStore.response}
      {onFiltersChange}
    />
  {/snippet}
  {#snippet main()}
    {#if router.current.route === "library"}
      <Library
        {mediaStore}
        {albumsStore}
        {hiddenStore}
        {toastStore}
        {appConfig}
        {activeFilters}
        {tagLabels}
        {onFiltersChange}
      />
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
      <Settings />
    {:else if router.current.route === "settings.ai"}
      <SettingsAI {appConfig} />
    {:else if router.current.route === "admin.settings.ai"}
      <AdminSettingsAI />
    {:else if router.current.route === "search"}
      <Search {events} {activeFilters} {tagLabels} {onFiltersChange} />
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
        {activeFilters}
        {tagLabels}
        {onFiltersChange}
      />
    {:else}
      <NotFound />
    {/if}
  {/snippet}
</ThreeColumnLayout>
<ToastStack {toastStore} />

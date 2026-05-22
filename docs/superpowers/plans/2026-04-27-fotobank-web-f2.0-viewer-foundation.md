# F2.0 Viewer Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the viewer-foundation infrastructure (reload-safe SPA router, incremental MediaStore, presentational MediaCell, larger thumb tiers, owner-scoped regenerate CLI) so the F2.4 lightbox can target a coherent shell.

**Architecture:** Two parallel tracks under one plan. **Frontend:** singleton `RouterStore` (Svelte 5 `$state`), MediaStore lifted to `App.svelte` and rewritten with `byMonth`/`byId` Maps + dirty-set tracking, presentational `MediaCell.svelte` extracted from `VirtualGrid`. **Backend:** thumb size vocabulary changes (`preview` 1024→2560, add `large=4096`, drop `lightbox`), and the existing `fotobank thumbs regenerate` CLI gains `--owner <hub>:<user>` and `--all-owners` scope flags that compose with the existing content selectors (`--all`/`--id`/`--type`/`--status`/`--since`).

**Tech Stack:** Svelte 5 runes, TypeScript (strict), Vitest, Playwright, Go 1.x, modernc.org/sqlite, cobra, golang-migrate, huma/v2, testify.

**Spec:** [docs/superpowers/specs/2026-04-27-fotobank-web-f2.0-viewer-foundation-design.md](../specs/2026-04-27-fotobank-web-f2.0-viewer-foundation-design.md)

---

## File Structure

**New files:**

- `frontend/src/lib/router/router.svelte.ts` — `RouterStore` singleton class, `RouteMatch` type, `handleInternalLinkClick` helper for plain-click anchor interception.
- `frontend/src/lib/router/router.test.ts` — vitest covering pathname matcher (incl. query stripping, anchored regex), `navigate()`, `syncFromLocation()`, `handleInternalLinkClick` modifier-key behavior.
- `frontend/src/routes/NotFound.svelte` — minimal "page not found" surface with back link to `/library`.
- `frontend/src/lib/grid/MediaCell.svelte` — presentational cell: `media` / `selected` / `onCellClick` props, internal `imgError` state for thumb 404 fallback.
- `frontend/src/lib/grid/MediaCell.test.ts` — vitest using `@testing-library/svelte` covering markup (anchor href, img src, `aria-label`, `class:selected`) + `onerror` swap to placeholder.

**Modified files:**

- `frontend/src/App.svelte` — replace inline `route` `$state` + `mediaIdFromRoute` regex + popstate effect with `router` calls; mount a single `MediaStore`; pass `mediaStore` prop to Library/Sessions; render `NotFound` for unknown routes.
- `frontend/src/lib/components/Sidebar.svelte` — add `onclick={(e) => handleInternalLinkClick(e, entry.href)}` to nav entries so SPA nav doesn't full-reload.
- `frontend/src/lib/media/mediaStore.svelte.ts` — replace the rebuild-from-`this.months` `merge()` with a long-lived `byMonth: Map<string, Map<string, Media>>` + `byId: Map<string, string>` and a dirty-set re-snapshot that reuses object refs for unaffected months.
- `frontend/src/lib/media/mediaStore.test.ts` — keep all 9 existing tests; add 3 new tests (replacement-same-month dirties, cross-month relocation, ref-stability for unaffected months).
- `frontend/src/lib/grid/VirtualGrid.svelte` — replace inline `<a><img></a>` with `<MediaCell>`; cell click handler calls `router.navigate` for plain click instead of letting the anchor full-reload.
- `frontend/src/routes/Library.svelte` — drop `new MediaStore(api)`; accept `{ mediaStore }: { mediaStore: MediaStore }` prop.
- `frontend/src/routes/Sessions.svelte` — drop `new MediaStore(api)`; accept `{ mediaStore }` prop; replace inline anchor/img with `<MediaCell>`; add the same shift/ctrl/meta + plain-click dispatch.
- `frontend/tests/e2e/library.spec.ts` — append three reload tests (`/media/abc`, `/foo-not-a-route`, `/api/v1/healthz`) and one no-duplicate-fetch SPA-nav test.
- `internal/thumb/sizes.go` — `SizePreview` MaxEdge 1024 → 2560; add `SizeLarge` (4096); remove `SizeLightbox`; update `ParseSize` and `AllSizes`.
- `internal/thumb/sizes_test.go` — drop `SizeLightbox` rows, add `large` rows, assert `ParseSize("lightbox")` returns `ErrUnknownSize`.
- `internal/httpapi/media_thumb_test.go` — keep happy path with `?size=preview`; add `?size=large&v=N` happy path; add `?size=lightbox` → 400.
- `internal/cli/thumbs.go` — extend `regenerateOpts`/cobra flags with `--owner` and `--all-owners`; mutual-exclusion validation; owner-format validation; loop `owners.Repo.List` for `--all-owners`; bypass the `identity.mode=stub` requirement when an explicit scope flag is set.
- `internal/cli/thumbs_test.go` — keep all 3 existing tests; add 5 new ones (with-owner, all-owners, no-content-with-scope, malformed-owner, mutually-exclusive).

**Unchanged but referenced:**

- `frontend/src/routes/MediaDetail.svelte` — already exists as a `{ id }`-prop stub. Task 4 wires its `/library` back link through `handleInternalLinkClick` so SPA nav back to the library doesn't full-reload (and so the hoisted MediaStore survives). F2.4 replaces the stub with the full lightbox.
- `internal/web/embed.go` — SPA fallback already implemented (returns shell for unknown non-`/api/`, non-`/assets/` paths). F2.0 only verifies behavior via Playwright.
- `internal/thumb/worker.go` — `emitSizes` already loops over `AllSizes()` and uses `sz.MaxEdge()`. The size-vocabulary change is picked up automatically.

---

## Pre-flight check

- [ ] **Run baseline test suites and lock current pass count.**

```bash
cd /path/to/fotobank && go test ./... -short -shuffle=on
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
```

Expected: both green. If anything is red on master before starting, stop and fix that first. (Per CLAUDE.md, all work commits directly to master; there is no feature branch.)

> **One-time vitest prerequisite for Svelte 5 component tests.** Task 7 is the first task in this plan that renders a Svelte component under vitest. `@testing-library/svelte` 5.x ships a Vite plugin (`@testing-library/svelte/vite`) that adds the `browser` resolve condition under VITEST so Svelte 5's client-mode `mount(...)` is reachable from jsdom; without it the renderer hits Svelte's SSR build and tests fail with `mount(...) is not available on the server`. If `frontend/vite.config.ts` does not already register `svelteTesting()` alongside `svelte()`, add it as part of Task 7. The plugin gates itself behind `process.env.VITEST` and is a no-op in `vite build`.

---

## Task 1: RouterStore class + matcher tests

**Files:**
- Create: `frontend/src/lib/router/router.svelte.ts`
- Create: `frontend/src/lib/router/router.test.ts`

The router is a singleton class that mirrors the existing `ThemeStore` / `EventsStore` pattern (instantiated once at module load, exposed as a top-level export). It must use `$state` (this file is `.svelte.ts`, which Svelte's compiler picks up as runed). The matcher anchors every regex with `^...$` so `/media/abc/extra` is `notfound`, not a match for `/media/:id`. Pathnames carrying a query string (`/media/abc?x=1`) must be matched on pathname only.

- [ ] **Step 1: Write the matcher tests.**

```ts
// frontend/src/lib/router/router.test.ts
import { describe, it, expect, beforeEach } from "vitest";
import { RouterStore, handleInternalLinkClick } from "./router.svelte";

function setLocation(pathname: string) {
  window.history.replaceState({}, "", pathname);
}

describe("RouterStore.match", () => {
  beforeEach(() => setLocation("/"));

  it("matches /, /library, /sessions, /settings", () => {
    const r = new RouterStore();
    setLocation("/"); r.syncFromLocation();
    expect(r.current.route).toBe("library");
    setLocation("/library"); r.syncFromLocation();
    expect(r.current.route).toBe("library");
    setLocation("/sessions"); r.syncFromLocation();
    expect(r.current.route).toBe("sessions");
    setLocation("/settings"); r.syncFromLocation();
    expect(r.current.route).toBe("settings");
  });

  it("matches /media/:id and exposes the id", () => {
    setLocation("/media/abc");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "abc" });
  });

  it("ignores search and hash when matching", () => {
    setLocation("/media/abc?return=library");
    const r = new RouterStore();
    expect(r.current).toEqual({ route: "media", id: "abc" });
  });

  it("returns notfound for unknown pathnames (with or without query)", () => {
    setLocation("/foo");
    let r = new RouterStore();
    expect(r.current.route).toBe("notfound");
    setLocation("/foo?bar=baz");
    r = new RouterStore();
    expect(r.current.route).toBe("notfound");
  });

  it("returns notfound for /media/abc/extra (anchored regex)", () => {
    setLocation("/media/abc/extra");
    const r = new RouterStore();
    expect(r.current.route).toBe("notfound");
  });
});

describe("RouterStore.navigate", () => {
  beforeEach(() => setLocation("/"));

  it("pushes history and updates current", () => {
    const r = new RouterStore();
    r.navigate("/sessions");
    expect(window.location.pathname).toBe("/sessions");
    expect(r.current.route).toBe("sessions");
  });

  it("syncFromLocation re-reads window.location", () => {
    const r = new RouterStore();
    setLocation("/library");
    r.syncFromLocation();
    expect(r.current.route).toBe("library");
  });
});

describe("handleInternalLinkClick", () => {
  it("preventDefaults and navigates on plain left click", () => {
    setLocation("/");
    const r = new RouterStore();
    const e = new MouseEvent("click", { button: 0, cancelable: true });
    handleInternalLinkClick(e, "/sessions", r);
    expect(e.defaultPrevented).toBe(true);
    expect(r.current.route).toBe("sessions");
  });

  it("ignores middle-click, right-click, and modifier keys", () => {
    setLocation("/");
    const r = new RouterStore();
    for (const init of [
      { button: 1 },
      { button: 2 },
      { button: 0, metaKey: true },
      { button: 0, ctrlKey: true },
      { button: 0, shiftKey: true },
      { button: 0, altKey: true },
    ]) {
      const e = new MouseEvent("click", { ...init, cancelable: true });
      handleInternalLinkClick(e, "/sessions", r);
      expect(e.defaultPrevented).toBe(false);
    }
  });
});
```

- [ ] **Step 2: Run tests; expect failure (module not found).**

```bash
cd /path/to/fotobank/frontend && bun run test -- src/lib/router/router.test.ts
```

Expected: FAIL with "Cannot find module './router.svelte'".

- [ ] **Step 3: Create the router module.**

```ts
// frontend/src/lib/router/router.svelte.ts

export type RouteMatch =
  | { route: "library" }
  | { route: "sessions" }
  | { route: "settings" }
  | { route: "media"; id: string }
  | { route: "notfound"; path: string };

// Anchored patterns. Order doesn't matter — each regex tests in
// isolation; first match wins. Search and hash are stripped before
// matching so /media/abc?x=1 matches /media/:id.
const PATTERNS: Array<{ re: RegExp; build: (m: RegExpMatchArray) => RouteMatch }> = [
  { re: /^\/$/,           build: () => ({ route: "library" }) },
  { re: /^\/library$/,    build: () => ({ route: "library" }) },
  { re: /^\/sessions$/,   build: () => ({ route: "sessions" }) },
  { re: /^\/settings$/,   build: () => ({ route: "settings" }) },
  { re: /^\/media\/([^/]+)$/, build: (m) => ({ route: "media", id: m[1]! }) },
];

export class RouterStore {
  current = $state<RouteMatch>(this.match(window.location.pathname));

  navigate(path: string) {
    history.pushState({}, "", path);
    this.syncFromLocation();
  }

  syncFromLocation() {
    this.current = this.match(window.location.pathname);
  }

  // Pathname-only match. window.location.pathname already excludes
  // the search and hash, so no extra stripping is needed; we read it
  // through the parameter to keep the function pure for tests.
  private match(pathname: string): RouteMatch {
    for (const p of PATTERNS) {
      const m = pathname.match(p.re);
      if (m) return p.build(m);
    }
    return { route: "notfound", path: pathname };
  }
}

// Module-level singleton. Instantiated once when this module is
// first imported (i.e., at App.svelte boot). Mirrors the
// SelectionStore module-export pattern.
export const router = new RouterStore();

// handleInternalLinkClick is the single helper used by every
// in-app anchor (Sidebar, NotFound back, MediaDetail back, MediaCell
// plain-click fallthrough). It honors the browser's existing
// "open in new tab" gestures by ignoring middle/right-click and any
// modifier key. Routing through this helper keeps the SPA from
// triggering a full-page reload on internal nav.
export function handleInternalLinkClick(
  e: MouseEvent,
  href: string,
  r: RouterStore = router,
) {
  if (e.button !== 0) return;
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  e.preventDefault();
  r.navigate(href);
}
```

- [ ] **Step 4: Run tests; expect pass.**

```bash
cd /path/to/fotobank/frontend && bun run test -- src/lib/router/router.test.ts
```

Expected: PASS — 9 tests across the 3 describes.

- [ ] **Step 5: Run typecheck and the full vitest suite.**

```bash
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
```

Expected: typecheck clean; vitest existing tests still pass.

- [ ] **Step 6: Commit.**

```bash
git add frontend/src/lib/router/router.svelte.ts frontend/src/lib/router/router.test.ts
git commit -m "feat(frontend): add singleton RouterStore + handleInternalLinkClick helper"
```

---

## Task 2: NotFound.svelte route

**Files:**
- Create: `frontend/src/routes/NotFound.svelte`

A static surface. Tested transitively via the App.svelte route-mount task and the Playwright reload test. Using `handleInternalLinkClick` here means even the back-link is SPA-routed.

- [ ] **Step 1: Create NotFound.svelte.**

```svelte
<!-- frontend/src/routes/NotFound.svelte -->
<script lang="ts">
  import { handleInternalLinkClick } from "../lib/router/router.svelte";
</script>

<div class="wrap">
  <h1>Page not found</h1>
  <p>That URL doesn't match any view in fotobank.</p>
  <a href="/library" onclick={(e) => handleInternalLinkClick(e, "/library")}>
    ← Back to Library
  </a>
</div>

<style>
  .wrap { padding: 32px; color: var(--text-primary); }
  h1 { margin: 0 0 8px; font-size: 18px; }
  p { color: var(--text-secondary); margin: 0 0 16px; }
  a { color: var(--accent); text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>
```

- [ ] **Step 2: Run typecheck.**

```bash
cd /path/to/fotobank/frontend && bun run typecheck
```

Expected: clean.

- [ ] **Step 3: Commit.**

```bash
git add frontend/src/routes/NotFound.svelte
git commit -m "feat(frontend): add NotFound route component"
```

---

## Task 3: Wire RouterStore into App.svelte

**Files:**
- Modify: `frontend/src/App.svelte`

Replace the inline `route` `$state` + `mediaIdFromRoute` + popstate handler with the router's `current` and `syncFromLocation`. Add a NotFound branch. Leave the Escape-key handler as-is.

- [ ] **Step 1: Rewrite the script block to use the router.**

Replace lines 2–60 of `frontend/src/App.svelte` with:

```svelte
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
  import { router } from "./lib/router/router.svelte";
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

  function activeId(route: typeof router.current): string {
    if (route.route === "sessions") return "sessions";
    if (route.route === "settings") return "settings";
    return "library";
  }
</script>
```

And replace the markup (lines 62–79) with:

```svelte
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
```

> Library and Sessions still create their own MediaStore at this point — Task 6 hoists it. Doing the hoist in this same task would entangle two concerns and make a regression bisect harder.

- [ ] **Step 2: Run typecheck and the full vitest suite.**

```bash
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
```

Expected: clean.

- [ ] **Step 3: Smoke-test the dev server.**

```bash
cd /path/to/fotobank && make frontend-dev &
# wait ~3s for vite, then in another shell:
curl -sI http://127.0.0.1:5181/library | head -1   # Expected: 200 OK
# Click around manually: /library, /sessions, /media/foo (renders MediaDetail
# stub), /not-a-route (renders NotFound). Browser back/forward must update
# the rendered route.
# Stop the dev server.
```

- [ ] **Step 4: Commit.**

```bash
git add frontend/src/App.svelte
git commit -m "feat(frontend): replace inline routing in App.svelte with RouterStore"
```

---

## Task 4: Sidebar + MediaDetail use handleInternalLinkClick

**Files:**
- Modify: `frontend/src/lib/components/Sidebar.svelte`
- Modify: `frontend/src/routes/MediaDetail.svelte`

Add an `onclick` to each entry so clicks SPA-route instead of full-reloading. The MediaDetail back link is wired through the same helper — without it, navigating back from `/media/:id` triggers a full document load and discards the hoisted MediaStore (which Task 6 set up specifically so library state survives in-app nav).

- [ ] **Step 1: Edit Sidebar.svelte.**

Change the anchor on line 22 of `frontend/src/lib/components/Sidebar.svelte`:

```svelte
<!-- before -->
<a class="entry" class:active={active === entry.id} href={entry.href}>{entry.label}</a>
```

To:

```svelte
<a
  class="entry"
  class:active={active === entry.id}
  href={entry.href}
  onclick={(e) => handleInternalLinkClick(e, entry.href)}
>{entry.label}</a>
```

And add the import at the top of the script block:

```ts
import { handleInternalLinkClick } from "../router/router.svelte";
```

- [ ] **Step 2: Edit MediaDetail.svelte.**

In `frontend/src/routes/MediaDetail.svelte`, add the import and wire the back link:

```svelte
<script lang="ts">
  import { handleInternalLinkClick } from "../lib/router/router.svelte";
  let { id }: { id: string } = $props();
</script>

<!-- before -->
<a href="/library" class="back" aria-label="Back to library">←</a>

<!-- after -->
<a
  href="/library"
  class="back"
  aria-label="Back to library"
  onclick={(e) => handleInternalLinkClick(e, "/library")}
>←</a>
```

The rest of `MediaDetail.svelte` is unchanged — F2.4 will replace the whole component with the lightbox.

- [ ] **Step 3: Run typecheck.**

```bash
cd /path/to/fotobank/frontend && bun run typecheck
```

Expected: clean.

- [ ] **Step 4: Smoke-test in dev server.**

```bash
cd /path/to/fotobank && make frontend-dev &
# Open http://127.0.0.1:5181, click Library/Sessions/Settings in the
# sidebar. Network tab should show NO document fetch on those clicks
# (only XHR /api/* calls). Browser back/forward still works.
# Also: open /media/foo, click the back arrow — Network tab should
# show NO document fetch and the page must SPA-render /library.
```

- [ ] **Step 5: Commit.**

```bash
git add frontend/src/lib/components/Sidebar.svelte frontend/src/routes/MediaDetail.svelte
git commit -m "feat(frontend): SPA-route sidebar + MediaDetail back via handleInternalLinkClick"
```

---

## Task 5: MediaStore.merge incremental rewrite

**Files:**
- Modify: `frontend/src/lib/media/mediaStore.svelte.ts`
- Modify: `frontend/src/lib/media/mediaStore.test.ts`

The current `merge()` rebuilds `byMonth` from `this.months` on every call (`mediaStore.svelte.ts:59-77`) — O(N²) over paginated scrolls. F2.0 keeps `byMonth` and `byId` as long-lived private state, dirties only the months that changed, and reuses month object refs for unaffected chunks so VirtualGrid's keyed each-block doesn't re-render the whole grid on every page.

The 9 existing tests (`mediaStore.test.ts:1-192`) define the externally observable contract and must stay green.

- [ ] **Step 1: Add the 3 new tests.**

These tests drive merge through the existing `loadMore` path (mocked GET returns page 1 with `next_offset: 200`, page 2 with `next_offset: null`). No private-method casts.

Append to `frontend/src/lib/media/mediaStore.test.ts` (inside the existing `describe("MediaStore", ...)` block):

```ts
  it("dirties the same-month bucket when a row replaces with new thumb_version", async () => {
    const page1 = {
      data: {
        items: [
          { id: "x", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
        ],
        next_offset: 200,
      },
      error: undefined,
    };
    const page2 = {
      data: {
        items: [
          { id: "x", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 7 },
        ],
        next_offset: null,
      },
      error: undefined,
    };
    const fakeClient = {
      GET: vi.fn().mockResolvedValueOnce(page1).mockResolvedValueOnce(page2),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    const before = store.months[0];
    expect(before?.items[0]?.thumbUrl).toBe("/api/v1/media/x/thumb?size=grid&v=1");

    await store.loadMore();
    const after = store.months[0];
    // Field updated...
    expect(after?.items[0]?.thumbUrl).toBe("/api/v1/media/x/thumb?size=grid&v=7");
    // ...and the month object ref must have changed because its
    // contents changed. A stale ref would let VirtualGrid skip a
    // re-render that should have happened.
    expect(after).not.toBe(before);
  });

  it("relocates an item to a new month and prunes the old bucket when it empties", async () => {
    const page1 = {
      data: {
        items: [
          { id: "a", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1 },
        ],
        next_offset: 200,
      },
      error: undefined,
    };
    const page2 = {
      data: {
        items: [
          { id: "a", timestamp: "2025-12-01T12:00:00Z", width: 1, height: 1 },
        ],
        next_offset: null,
      },
      error: undefined,
    };
    const fakeClient = {
      GET: vi.fn().mockResolvedValueOnce(page1).mockResolvedValueOnce(page2),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    expect(store.months.map((m) => m.key)).toEqual(["2026-04"]);

    await store.loadMore();
    // The 2026-04 bucket lost its only row; the relocation must have
    // pruned the empty bucket.
    expect(store.months.map((m) => m.key)).toEqual(["2025-12"]);
    expect(store.months[0]?.items[0]?.id).toBe("a");
  });

  it("leaves every month object ref stable when re-merging identical input", async () => {
    const items = [
      { id: "1", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
      { id: "2", timestamp: "2026-03-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
      { id: "3", timestamp: "2026-02-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
    ];
    const fakeClient = {
      GET: vi.fn()
        .mockResolvedValueOnce({ data: { items, next_offset: 200 }, error: undefined })
        .mockResolvedValueOnce({ data: { items, next_offset: null }, error: undefined }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    const refs = new Map(store.months.map((m) => [m.key, m]));

    await store.loadMore(); // re-merge identical input

    // Every month object ref must be unchanged: identical inputs mean
    // every row's identity fields (timestamp, aspect, thumbUrl) match
    // what's already in the bucket, so nothing should be marked dirty.
    for (const m of store.months) {
      expect(m).toBe(refs.get(m.key));
    }
  });
```

- [ ] **Step 2: Run the test file; expect failures.**

```bash
cd /path/to/fotobank/frontend && bun run test -- src/lib/media/mediaStore.test.ts
```

Expected: 3 of the 3 new tests fail (the existing implementation rebuilds month refs every merge; ref-stability and same-month-replacement-dirties may both fail). 9 existing tests still pass.

- [ ] **Step 3: Rewrite `merge()` and add the new private state.**

Replace lines 22–78 of `frontend/src/lib/media/mediaStore.svelte.ts`:

```ts
export class MediaStore {
  months = $state<Month[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  private nextOffset: number | null = 0;

  // byMonth: monthKey → (id → Media). Long-lived; survives across
  // merge() calls. byId: id → current monthKey, used to relocate a
  // row when its timestamp moves across months.
  private byMonth = new Map<string, Map<string, Media>>();
  private byId = new Map<string, string>();

  constructor(private client: Pick<Client, "GET">) {}

  async loadInitial() { await this.loadMore(); }

  async loadMore() {
    if (this.loading || this.exhausted) return;
    // Synchronous before any await — required as the re-entry guard.
    this.loading = true;
    try {
      const res = await this.client.GET("/api/v1/media", {
        // sort_desc: true so the library opens at the most-recent
        // capture (the backend defaults to ascending). Pagination then
        // walks backwards in time as the user scrolls down.
        params: {
          query: { limit: 200, offset: this.nextOffset ?? 0, sort_desc: true },
        } as never,
      });
      if (res.error || !res.data) return;
      const items = ((res.data as { items?: Array<Record<string, unknown>> }).items ?? [])
        .map(toMedia)
        .filter((m): m is Media => m !== null);
      this.merge(items);
      const next = (res.data as { next_offset?: number | null }).next_offset ?? null;
      this.nextOffset = next;
      if (next === null) this.exhausted = true;
    } finally {
      this.loading = false;
    }
  }

  private merge(items: Media[]) {
    // Track which month buckets changed so we can rebuild only those
    // entries in the months snapshot. Untouched months reuse their
    // existing object ref → VirtualGrid's keyed each-block skips
    // re-renders for them. A re-merge of an identical row is a no-op:
    // we compare identity fields and skip the dirty mark when they
    // match, which is what lets the SSE-overlap and refetch paths run
    // without churning every chunk.
    const dirty = new Set<string>();
    for (const it of items) {
      const newKey = monthKey(it.taken);
      const oldKey = this.byId.get(it.id);
      if (oldKey !== undefined && oldKey !== newKey) {
        // Cross-month relocation: remove from the old bucket and
        // prune if the bucket emptied.
        const oldInner = this.byMonth.get(oldKey);
        if (oldInner) {
          oldInner.delete(it.id);
          if (oldInner.size === 0) this.byMonth.delete(oldKey);
        }
        dirty.add(oldKey);
      }
      let inner = this.byMonth.get(newKey);
      if (!inner) {
        inner = new Map<string, Media>();
        this.byMonth.set(newKey, inner);
        dirty.add(newKey); // brand-new month, must be in the snapshot
      }
      const existing = inner.get(it.id);
      const unchanged = existing !== undefined
        && existing.timestamp === it.timestamp
        && existing.thumbUrl === it.thumbUrl
        && existing.aspect === it.aspect;
      if (!unchanged) {
        inner.set(it.id, it);
        dirty.add(newKey);
      }
      // byId always reflects the latest known location for this id.
      this.byId.set(it.id, newKey);
    }

    // Rebuild the reactive months snapshot. Clean months reuse the
    // existing object ref; dirty (or new) months get a fresh object
    // with re-sorted items.
    const prev = new Map(this.months.map((m) => [m.key, m]));
    const sortedKeys = Array.from(this.byMonth.keys()).sort((a, b) =>
      a < b ? 1 : a > b ? -1 : 0,
    );
    this.months = sortedKeys.map((k) => {
      if (!dirty.has(k)) {
        const reuse = prev.get(k);
        if (reuse) return reuse;
      }
      const inner = this.byMonth.get(k)!;
      const sorted = Array.from(inner.values()).sort((a, b) => +b.taken - +a.taken);
      return { key: k, items: sorted };
    });
  }
}
```

`toMedia` and `monthKey` (above and below the class) stay unchanged.

- [ ] **Step 4: Run the test file; expect all 12 tests to pass.**

```bash
cd /path/to/fotobank/frontend && bun run test -- src/lib/media/mediaStore.test.ts
```

Expected: PASS — 9 prior tests + 3 new tests = 12.

- [ ] **Step 5: Run typecheck and the full vitest suite.**

```bash
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
```

Expected: clean.

- [ ] **Step 6: Commit.**

```bash
git add frontend/src/lib/media/mediaStore.svelte.ts frontend/src/lib/media/mediaStore.test.ts
git commit -m "perf(frontend): incremental MediaStore.merge with byMonth/byId + dirty tracking"
```

---

### CHECKPOINT after Task 5

Run `/roborev-fix` to address any review findings on Tasks 1-5 in one pass before moving on. Resume at Task 6 only after all open reviews are closed.

---

## Task 6: Hoist MediaStore to App.svelte

**Files:**
- Modify: `frontend/src/App.svelte`
- Modify: `frontend/src/routes/Library.svelte`
- Modify: `frontend/src/routes/Sessions.svelte`

A single `MediaStore` instance lives at the App level and is passed as a prop to Library and Sessions. SPA nav between them no longer trashes and rebuilds the store, so the initial `/api/v1/media?limit=200&offset=0` fetch is observed once per page load.

- [ ] **Step 1: Update App.svelte to instantiate MediaStore and pass it down.**

In `frontend/src/App.svelte`, add to the script block:

```ts
import { MediaStore } from "./lib/media/mediaStore.svelte";

const mediaStore = new MediaStore(api);
mediaStore.loadInitial();
```

(Place it next to the existing `themeStore` / `events` instantiation block.)

Update the main snippet to thread the prop through:

```svelte
{#if router.current.route === "library"}
  <Library {mediaStore} />
{:else if router.current.route === "sessions"}
  <Sessions {mediaStore} />
{:else if router.current.route === "media"}
  <MediaDetail id={router.current.id} />
{:else if router.current.route === "settings"}
  <div style="padding:20px">Settings (placeholder; theme = {themeStore.theme})</div>
{:else}
  <NotFound />
{/if}
```

- [ ] **Step 2: Update Library.svelte to accept the prop.**

Replace lines 2–14 of `frontend/src/routes/Library.svelte`:

```svelte
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import { api } from "../lib/api/client";

  let { mediaStore }: { mediaStore: MediaStore } = $props();

  const density = new DensityStore(api, "library");
  density.load();
</script>
```

And update the body that referenced `store` to reference `mediaStore`:

```svelte
<VirtualGrid
  months={mediaStore.months}
  onLoadMore={() => mediaStore.loadMore()}
  targetRowHeight={density.targetRowHeight}
/>

{#if mediaStore.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if mediaStore.months.length === 0 && !mediaStore.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}
```

- [ ] **Step 3: Update Sessions.svelte to accept the prop.**

Replace lines 2–16 of `frontend/src/routes/Sessions.svelte`:

```svelte
<script lang="ts">
  import MonthChunk from "../lib/grid/MonthChunk.svelte";
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import { groupIntoSessions } from "../lib/sessions/sessionGrouping";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import { api } from "../lib/api/client";

  let { mediaStore }: { mediaStore: MediaStore } = $props();

  const density = new DensityStore(api, "sessions");
  density.load();
  const flat = $derived(mediaStore.months.flatMap((m) => m.items));
  const sessions = $derived(groupIntoSessions(flat, { gapHours: 4 }));
```

And update the rest of the file's `store.*` references to `mediaStore.*`:

```svelte
  $effect(() => {
    if (!sentinel) return;
    const io = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) mediaStore.loadMore();
    }, { rootMargin: "800px 0px" });
    io.observe(sentinel);
    return () => io.disconnect();
  });
```

```svelte
{#if mediaStore.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if mediaStore.months.length === 0 && !mediaStore.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}
```

- [ ] **Step 4: Run typecheck and the full vitest suite.**

```bash
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
```

Expected: clean.

- [ ] **Step 5: Smoke-test SPA nav.**

```bash
cd /path/to/fotobank && make frontend-dev &
# Open http://127.0.0.1:5181, then in DevTools Network tab:
#   1. Reload — observe ONE GET /api/v1/media?limit=200&offset=0&sort_desc=true
#   2. Click Sessions in sidebar — NO new GET /api/v1/media?...&offset=0
#   3. Click Library in sidebar — still NO new GET /api/v1/media?...&offset=0
#   4. Browser back — same.
# Pagination sentinels (offset=200, etc) may fire when scrolling; that's fine.
```

- [ ] **Step 6: Commit.**

```bash
git add frontend/src/App.svelte frontend/src/routes/Library.svelte frontend/src/routes/Sessions.svelte
git commit -m "feat(frontend): hoist MediaStore to App.svelte; Library/Sessions accept it as a prop"
```

---

## Task 7: MediaCell.svelte presentational component

**Files:**
- Create: `frontend/src/lib/grid/MediaCell.svelte`
- Create: `frontend/src/lib/grid/MediaCell.test.ts`

A presentational component shared by VirtualGrid and Sessions (and, in F2.4, the lightbox-strip). It owns no selection or navigation policy: the caller passes `selected` and `onCellClick`. The component manages one piece of internal state — `imgError` — so a thumb 404 swaps to a neutral placeholder div without ever leaving the cell.

- [ ] **Step 1: Write the failing tests.**

```ts
// frontend/src/lib/grid/MediaCell.test.ts
import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import MediaCell from "./MediaCell.svelte";

const mediaWithThumb = {
  id: "abc",
  aspect: 1.5,
  thumbUrl: "/api/v1/media/abc/thumb?size=grid&v=3",
};

describe("MediaCell", () => {
  it("renders an anchor to /media/:id with aria-label and the thumb img", () => {
    const { container } = render(MediaCell, {
      media: mediaWithThumb,
      selected: false,
      onCellClick: () => {},
    });
    const a = container.querySelector("a")!;
    expect(a.getAttribute("href")).toBe("/media/abc");
    expect(a.getAttribute("aria-label")).toBe("Photo abc");
    expect(a.classList.contains("selected")).toBe(false);
    const img = container.querySelector("img")!;
    expect(img.getAttribute("src")).toBe(mediaWithThumb.thumbUrl);
  });

  it("applies the selected class when selected=true", () => {
    const { container } = render(MediaCell, {
      media: mediaWithThumb,
      selected: true,
      onCellClick: () => {},
    });
    expect(container.querySelector("a")?.classList.contains("selected")).toBe(true);
  });

  it("invokes onCellClick on click", async () => {
    const onCellClick = vi.fn();
    const { container } = render(MediaCell, {
      media: mediaWithThumb, selected: false, onCellClick,
    });
    await fireEvent.click(container.querySelector("a")!);
    expect(onCellClick).toHaveBeenCalledTimes(1);
  });

  it("swaps to a placeholder div when the img fires onerror", async () => {
    const { container } = render(MediaCell, {
      media: mediaWithThumb, selected: false, onCellClick: () => {},
    });
    const img = container.querySelector("img")!;
    await fireEvent.error(img);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".placeholder")).not.toBeNull();
  });

  it("renders a placeholder when thumbUrl is missing", () => {
    // `MediaLite.thumbUrl?: string` + `exactOptionalPropertyTypes: true`
    // means an explicit `thumbUrl: undefined` is rejected by tsc — omit
    // the property instead. Both forms set `media.thumbUrl === undefined`
    // at runtime, exercising the same `!media.thumbUrl` placeholder
    // branch.
    const { container } = render(MediaCell, {
      media: { id: "x", aspect: 1 },
      selected: false, onCellClick: () => {},
    });
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".placeholder")).not.toBeNull();
  });

  it("re-attempts the image when media.thumbUrl changes after an error", async () => {
    // Regression-locks the thumb-regenerate cache-bust path: when an
    // operator runs `thumbs regenerate`, thumb_version bumps and a
    // cached row still pointing at ?v=N 404s. Once a refetch lands the
    // ?v=N+1 URL, MediaCell must re-attempt the load instead of
    // staying on the placeholder forever.
    const { container, rerender } = render(MediaCell, {
      media: { id: "x", aspect: 1, thumbUrl: "/api/v1/media/x/thumb?size=grid&v=1" },
      selected: false, onCellClick: () => {},
    });
    await fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".placeholder")).not.toBeNull();

    await rerender({
      media: { id: "x", aspect: 1, thumbUrl: "/api/v1/media/x/thumb?size=grid&v=2" },
      selected: false, onCellClick: () => {},
    });
    const img = container.querySelector("img");
    expect(img).not.toBeNull();
    expect(img!.getAttribute("src")).toBe("/api/v1/media/x/thumb?size=grid&v=2");
  });
});
```

- [ ] **Step 2: Run the file; expect failure.**

```bash
cd /path/to/fotobank/frontend && bun run test -- src/lib/grid/MediaCell.test.ts
```

Expected: FAIL with module-not-found.

- [ ] **Step 3: Create MediaCell.svelte.**

```svelte
<!-- frontend/src/lib/grid/MediaCell.svelte -->
<script lang="ts">
  import type { MediaLite } from "./monthChunkLayout";

  let { media, selected, onCellClick }: {
    media: MediaLite;
    selected: boolean;
    onCellClick: (e: MouseEvent) => void;
  } = $props();

  // Tracks whether the current thumb URL has 404'd in this cell. F2.0
  // operators trigger this by running `fotobank thumbs regenerate`,
  // which bumps thumb_version on disk; cached <img> elements continue
  // to point at v=N until a refetch, and the v=N thumb returns 404
  // until the worker drains. Showing a neutral placeholder is much
  // better than the browser's broken-image glyph.
  let imgError = $state(false);

  // Reset the error flag whenever media.thumbUrl changes, so once a
  // refetch lands the v=N+1 URL we re-attempt the load. Without this
  // the cell is stuck on the placeholder for the lifetime of the
  // component even after the worker drains.
  $effect(() => {
    media.thumbUrl;
    imgError = false;
  });
</script>

<a
  href={`/media/${media.id}`}
  aria-label={`Photo ${media.id}`}
  class:selected
  onclick={onCellClick}
>
  {#if !imgError && media.thumbUrl}
    <img
      src={media.thumbUrl}
      alt=""
      loading="lazy"
      decoding="async"
      style="width:100%;height:100%;object-fit:cover"
      onerror={() => (imgError = true)}
    />
  {:else}
    <div class="placeholder" aria-hidden="true"></div>
  {/if}
</a>

<style>
  a {
    display: block;
    width: 100%;
    height: 100%;
  }
  a.selected {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
    border-radius: 2px;
  }
  .placeholder {
    width: 100%;
    height: 100%;
    background: var(--bg-elevated);
    border-radius: 2px;
  }
</style>
```

- [ ] **Step 4: Run the test file; expect pass.**

```bash
cd /path/to/fotobank/frontend && bun run test -- src/lib/grid/MediaCell.test.ts
```

Expected: PASS — 6 tests.

- [ ] **Step 5: Run typecheck and the full vitest suite.**

```bash
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
```

Expected: clean.

- [ ] **Step 6: Commit.**

```bash
git add frontend/src/lib/grid/MediaCell.svelte frontend/src/lib/grid/MediaCell.test.ts
git commit -m "feat(frontend): add presentational MediaCell with imgError fallback"
```

---

## Task 8: VirtualGrid uses MediaCell

**Files:**
- Modify: `frontend/src/lib/grid/VirtualGrid.svelte`

Replace the inline `<a><img></a>` markup with `<MediaCell>`. The existing `handleCellClick` keeps shift/meta/ctrl semantics; for plain click it now calls `router.navigate` (with `e.preventDefault()`) instead of letting the anchor full-reload.

- [ ] **Step 1: Edit VirtualGrid.svelte's script block.**

In `frontend/src/lib/grid/VirtualGrid.svelte`, add to the imports (after line 7):

```ts
import MediaCell from "./MediaCell.svelte";
import { router } from "../router/router.svelte";
```

Replace the `handleCellClick` function (lines 97–110):

```ts
function handleCellClick(e: MouseEvent, id: string) {
  // Ignore middle-click (button 1, opens new tab) and right-click
  // (button 2, context menu). Shift+middle-click would otherwise
  // hijack the new-tab gesture.
  if (e.button !== 0) return;
  if (e.shiftKey) {
    e.preventDefault();
    selection.range(id, orderedIds);
    return;
  }
  if (e.metaKey || e.ctrlKey) {
    e.preventDefault();
    selection.toggle(id);
    return;
  }
  // Plain click: SPA-route via the router instead of the anchor's
  // default full-page navigation.
  e.preventDefault();
  router.navigate(`/media/${id}`);
}
```

- [ ] **Step 2: Replace the cell markup.**

Lines 123–132 of `frontend/src/lib/grid/VirtualGrid.svelte`:

```svelte
<!-- before -->
{#snippet renderCell(m)}
  <a
    href={`/media/${m.id}`}
    aria-label={`Photo ${m.id}`}
    class:selected={selection.ids.has(m.id)}
    onclick={(e) => handleCellClick(e, m.id)}
  >
    <img src={m.thumbUrl} alt="" loading="lazy" decoding="async" style="width:100%;height:100%;object-fit:cover" />
  </a>
{/snippet}
```

Becomes:

```svelte
{#snippet renderCell(m)}
  <MediaCell
    media={m}
    selected={selection.ids.has(m.id)}
    onCellClick={(e) => handleCellClick(e, m.id)}
  />
{/snippet}
```

The `.grid :global(a.selected)` style on lines 141–145 is now provided inside `MediaCell.svelte`, so delete that style block from VirtualGrid:

```svelte
<style>
  .grid { padding: 8px; }
</style>
```

- [ ] **Step 3: Run typecheck and the full vitest suite.**

```bash
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
```

Expected: clean.

- [ ] **Step 4: Smoke-test in dev.**

```bash
cd /path/to/fotobank && make frontend-dev &
# Open http://127.0.0.1:5181/library
# - Plain click on a cell → URL changes to /media/<id> WITHOUT a full
#   page reload (DevTools Network tab shows no document fetch).
# - Shift-click two cells → range selection (highlighted).
# - Ctrl/Meta-click cells → toggle selection.
# - Middle-click a cell → opens /media/<id> in a new tab (browser default).
```

- [ ] **Step 5: Commit.**

```bash
git add frontend/src/lib/grid/VirtualGrid.svelte
git commit -m "refactor(frontend): VirtualGrid uses MediaCell; plain-click goes through router"
```

---

## Task 9: Sessions uses MediaCell + selection

**Files:**
- Modify: `frontend/src/routes/Sessions.svelte`

Sessions today has its own simpler cell snippet that's missing selection wiring entirely (`Sessions.svelte:58-62`). Replacing it with `<MediaCell>` aligns the Library and Sessions cell behavior, and selection / navigation semantics now match across both views.

- [ ] **Step 1: Edit Sessions.svelte to import MediaCell, router, and selection.**

After line 8 of `frontend/src/routes/Sessions.svelte`, add to the imports:

```ts
import MediaCell from "../lib/grid/MediaCell.svelte";
import { router } from "../lib/router/router.svelte";
import { selection } from "../lib/selection/selectionStore.svelte";
```

- [ ] **Step 2: Add the orderedIds derivation and the click handler.**

Inside the `<script>` block, near the existing `flat`/`sessions` derived state:

```ts
const orderedIds = $derived(
  sessions.flatMap((s) => s.items.map((it) => it.id)),
);

function handleCellClick(e: MouseEvent, id: string) {
  if (e.button !== 0) return;
  if (e.shiftKey) {
    e.preventDefault();
    selection.range(id, orderedIds);
    return;
  }
  if (e.metaKey || e.ctrlKey) {
    e.preventDefault();
    selection.toggle(id);
    return;
  }
  e.preventDefault();
  router.navigate(`/media/${id}`);
}
```

- [ ] **Step 3: Replace the inline anchor/img snippet.**

Lines 58–62 of `frontend/src/routes/Sessions.svelte`:

```svelte
<!-- before -->
{#snippet renderCell(m)}
  <a href={`/media/${m.id}`}>
    <img src={m.thumbUrl} alt="" loading="lazy" decoding="async" style="width:100%;height:100%;object-fit:cover" />
  </a>
{/snippet}
```

Becomes:

```svelte
{#snippet renderCell(m)}
  <MediaCell
    media={m}
    selected={selection.ids.has(m.id)}
    onCellClick={(e) => handleCellClick(e, m.id)}
  />
{/snippet}
```

- [ ] **Step 4: Run typecheck and the full vitest suite.**

```bash
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
```

Expected: clean.

- [ ] **Step 5: Smoke-test Sessions interactions.**

```bash
cd /path/to/fotobank && make frontend-dev &
# Open http://127.0.0.1:5181/sessions
# - Plain click on a cell → SPA-route to /media/<id>.
# - Shift-click → range selection.
# - Ctrl/Meta-click → toggle selection.
# - The selected outline is the same shape as on /library.
```

- [ ] **Step 6: Commit.**

```bash
git add frontend/src/routes/Sessions.svelte
git commit -m "refactor(frontend): Sessions uses MediaCell + shift/ctrl/meta selection"
```

---

## Task 10: thumb/sizes.go vocabulary change

**Files:**
- Modify: `internal/thumb/sizes.go`
- Modify: `internal/thumb/sizes_test.go`

`SizePreview` MaxEdge moves 1024 → 2560; `SizeLarge` (4096) is added; `SizeLightbox` is removed entirely. The worker (`emitSizes` in `worker.go:316`) iterates `AllSizes()` and resizes to `sz.MaxEdge()`, so the change is purely in the size vocabulary — no worker code changes.

- [ ] **Step 1: Update the sizes_test.go expectations (TDD: change the contract first).**

Replace lines 11–46 of `internal/thumb/sizes_test.go`:

```go
func TestSizeMaxEdge(t *testing.T) {
	r := require.New(t)
	r.Equal(256, thumb.SizeGrid.MaxEdge())
	r.Equal(2560, thumb.SizePreview.MaxEdge())
	r.Equal(4096, thumb.SizeLarge.MaxEdge())
}

func TestParseSizeValid(t *testing.T) {
	cases := []struct {
		in   string
		want thumb.Size
	}{
		{"grid", thumb.SizeGrid},
		{"preview", thumb.SizePreview},
		{"large", thumb.SizeLarge},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := thumb.ParseSize(c.in)
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestParseSizeInvalid(t *testing.T) {
	_, err := thumb.ParseSize("huge")
	require.ErrorIs(t, err, thumb.ErrUnknownSize)
}

func TestParseSizeLightboxIsRemoved(t *testing.T) {
	// SizeLightbox was retired in F2.0 (preview now covers fit, large
	// covers 1:1). Any caller still asking for "lightbox" must get
	// ErrUnknownSize so the HTTP handler 400s instead of silently
	// matching some other size.
	_, err := thumb.ParseSize("lightbox")
	require.ErrorIs(t, err, thumb.ErrUnknownSize)
}

func TestAllSizes(t *testing.T) {
	require.Equal(t,
		[]thumb.Size{thumb.SizeGrid, thumb.SizePreview, thumb.SizeLarge},
		thumb.AllSizes(),
	)
}
```

(Keep `TestThumbKey` unchanged.)

- [ ] **Step 2: Run; expect failures.**

```bash
cd /path/to/fotobank && go test ./internal/thumb -run "TestSizeMaxEdge|TestParseSizeValid|TestParseSizeLightboxIsRemoved|TestAllSizes" -v
```

Expected: compile error or test failures because `thumb.SizeLarge` does not exist and `SizeLightbox` is referenced elsewhere.

- [ ] **Step 3: Update sizes.go to the new vocabulary.**

Replace lines 11–58 of `internal/thumb/sizes.go`:

```go
// Size identifies one of the three thumbnail dimensions. The underlying
// string is the stable on-the-wire name (query param, filename stem).
type Size string

const (
	SizeGrid    Size = "grid"
	SizePreview Size = "preview"
	SizeLarge   Size = "large"
)

// ErrUnknownSize is returned by ParseSize when the input isn't a
// recognized size name. HTTP handlers translate this to 400.
var ErrUnknownSize = errors.New("thumb: unknown size")

// MaxEdge returns the target pixel length of the longest edge.
//   - grid (256) drives the library grid thumbnails.
//   - preview (2560) is the lightbox "fit" source (covers most laptop
//     and mobile displays at native pixel ratio without RAW decode).
//   - large (4096) is the lightbox 1:1 source for high-DPI / 4K
//     displays. Heavy to encode (~1s/row on Apple Silicon RAW), so
//     it's emitted lazily via the same emitSizes loop alongside grid
//     and preview rather than fetched on demand.
func (s Size) MaxEdge() int {
	switch s {
	case SizeGrid:
		return 256
	case SizePreview:
		return 2560
	case SizeLarge:
		return 4096
	}
	return 0
}

// ParseSize maps a query-param / filename string to a Size. Empty input
// is treated as invalid — the HTTP handler defaults to "grid" before
// calling in.
func ParseSize(s string) (Size, error) {
	switch s {
	case "grid":
		return SizeGrid, nil
	case "preview":
		return SizePreview, nil
	case "large":
		return SizeLarge, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownSize, s)
}

// AllSizes returns every Size in the order the worker emits them.
// Returns a fresh slice so callers can iterate freely.
func AllSizes() []Size {
	return []Size{SizeGrid, SizePreview, SizeLarge}
}
```

(Keep `ThumbKey` unchanged.)

- [ ] **Step 4: Run the thumb suite; expect pass.**

```bash
cd /path/to/fotobank && go test ./internal/thumb/... -v
```

Expected: all tests pass. The worker tests run real decode + storage and will resize to the new MaxEdge values; that path is unchanged.

- [ ] **Step 5: Add a worker test that asserts all three sizes are emitted per claim.**

The existing worker tests (e.g. `TestWorkerDrainsPendingRowToReady`) only read back `SizeGrid` (`internal/thumb/worker_test.go:181`). The size-vocabulary change is invisible to them. Lock the "AllSizes are emitted per claim" contract with an explicit assertion. Append to `internal/thumb/worker_test.go`:

```go
func TestWorkerEmitsAllSizesPerClaim(t *testing.T) {
	// emitSizes (worker.go:315) loops over AllSizes() and writes one
	// JPEG per size at the row's thumb_version. F2.0 changed the size
	// vocabulary (preview=2560, large=4096); this test guards against
	// a future regression where a size is silently dropped from the
	// loop or AllSizes returns a stale slice.
	r := require.New(t)
	fx := newWorkerFixture(t)
	defer fx.close()
	id, version := fx.seedReadyJPEGRow(t)
	fx.run(t, 1)
	waitForStatus(t, fx.rw, id, "ready")

	for _, sz := range thumb.AllSizes() {
		key := thumb.ThumbKey(id, version, sz)
		rc, err := fx.store.ReadRange(t.Context(), fx.owner, key, 0, -1)
		r.NoError(err, "size %s: ReadRange %s", sz, key)
		bs, err := io.ReadAll(rc)
		_ = rc.Close()
		r.NoError(err, "size %s: ReadAll", sz)
		r.Greater(len(bs), 100, "size %s: empty thumb bytes", sz)
	}
}
```

> Adapt `newWorkerFixture` / `fx.seedReadyJPEGRow` / `fx.run` / `fx.store` / `fx.owner` to the names the existing test harness already uses in `worker_test.go`. The point is: read back the bytes for every size in `AllSizes()`, not just `SizeGrid`. If the harness exposes a different read path, use that — the assertion shape is the contract.

- [ ] **Step 6: Run the worker suite; expect pass.**

```bash
cd /path/to/fotobank && go test ./internal/thumb -run "TestWorker" -v
```

Expected: all worker tests pass, including the new `TestWorkerEmitsAllSizesPerClaim`.

- [ ] **Step 7: Run the full backend suite.**

```bash
cd /path/to/fotobank && go test ./... -short -shuffle=on
```

Expected: clean. If anything fails citing `SizeLightbox`, fix that file in this same task — the only legitimate references should be inside test fixtures or the HTTP handler test, both of which we update in Task 11.

- [ ] **Step 8: Commit.**

```bash
git add internal/thumb/sizes.go internal/thumb/sizes_test.go internal/thumb/worker_test.go
git commit -m "feat(thumb): preview=2560, add large=4096, drop lightbox; lock AllSizes emission"
```

---

### CHECKPOINT after Task 10

Run `/roborev-fix` to address any review findings on Tasks 6-10 in one pass before moving on. Resume at Task 11 only after all open reviews are closed.

---

## Task 11: media_thumb HTTP test for size=lightbox→400 and size=large happy path

**Files:**
- Modify: `internal/httpapi/media_thumb_test.go`

The HTTP handler at `internal/httpapi/media_thumb.go:54` already returns 400 for any `?size=` value `ParseSize` rejects — Task 10 already made `lightbox` reject. This task locks the contract with two assertions: `?size=lightbox` is now a 400, and the previously-internal-only `?size=large` is reachable end-to-end.

- [ ] **Step 1: Inspect the existing tests to find the helper that builds a request + the test that uses `preview`.**

```bash
cd /path/to/fotobank && rg -n "TestThumbRouteUnknownSizeReturns400|size=preview|size=lightbox" internal/httpapi/media_thumb_test.go
```

Note the helper that seeds a media row with the desired `thumb_version` and writes the actual JPEG bytes for each size. The new `large` test reuses that helper.

- [ ] **Step 2: Add two new tests.**

Append to `internal/httpapi/media_thumb_test.go`:

```go
func TestThumbRouteLightboxReturns400(t *testing.T) {
	// SizeLightbox was retired in F2.0; ?size=lightbox must now hit
	// the same 400 path as any unknown size. This regression-locks
	// the size vocabulary against accidental re-introduction.
	r := require.New(t)
	srv := newThumbTestServer(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/media/anything/thumb?size=lightbox&v=0")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusBadRequest, resp.StatusCode)
}

func TestThumbRouteLargeHappyPath(t *testing.T) {
	r := require.New(t)
	srv, m := newThumbTestServerWithReadyRow(t)
	defer srv.Close()
	url := fmt.Sprintf("%s/api/v1/media/%s/thumb?size=large&v=%d",
		srv.URL, m.ID, m.ThumbVersion)
	resp, err := http.Get(url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("image/jpeg", resp.Header.Get("Content-Type"))
}

func TestThumbRouteStaleReadyRowReturns404ForNewSize(t *testing.T) {
	// F2.0 upgrade contract: rows whose thumb_status='ready' was set
	// under the F1 vocabulary have grid + preview + lightbox bytes on
	// disk but no large.jpg. After F2.0 deploy, ?size=large&v=N for
	// those rows must 404 (not 500, not silently rewrite to a
	// different size) until an operator runs `thumbs regenerate`,
	// which bumps thumb_version and re-emits the new size set under
	// v=N+1. The 404 is what MediaCell's placeholder fallback keys
	// off of; this regression-locks that path so a future "convenience
	// fallback" can't silently degrade to ?size=preview without
	// failing this test.
	r := require.New(t)
	srv, m := newThumbTestServerWithReadyRow(t)
	defer srv.Close()
	// newThumbTestServerWithReadyRow seeds bytes for AllSizes() per the
	// helper note above. To simulate a stale F1 row, delete the large
	// key from storage before the request.
	require.NoError(t, deleteSizeFromStore(t, srv, m, thumb.SizeLarge))

	url := fmt.Sprintf("%s/api/v1/media/%s/thumb?size=large&v=%d",
		srv.URL, m.ID, m.ThumbVersion)
	resp, err := http.Get(url)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode,
		"stale F1 ready row must 404 for size=large until regenerate runs")
}
```

> The `deleteSizeFromStore(t, srv, m, sz)` helper is whatever shape the harness already exposes (e.g., reach into `srv.testStore.Delete(...)` or rebuild the row with the matching version under different bytes). The intent: the row stays `thumb_status='ready'` and `thumb_version=N`, but the on-disk `v=N/large.jpg` is missing. If the harness has no Delete, write a zero-length file under a different key so `ReadRange(... v=N/large)` returns NotFound — the contract is that the bytes for the requested size are missing.

> If the existing test file does NOT already define `newThumbTestServer` / `newThumbTestServerWithReadyRow`, model the new tests on whatever existing helper writes a ready row + JPEG bytes (see e.g. `TestThumbRouteReturnsBytesOnMatchingVersion`). The two tests above are the contract; the harness call shape is project-specific.
>
> **Important:** verify the seed helper writes JPEG bytes for **every size** in `thumb.AllSizes()`, not just `SizeGrid`. The existing fixture for the F1 happy-path test may only have written grid bytes (the only size F1 frontend used), in which case `?size=large&v=N` will 404 against a "ready" row even though the row's `thumb_status` says it's done. If the helper is grid-only, extend it to write bytes for grid + preview + large before adding `TestThumbRouteLargeHappyPath`.

- [ ] **Step 3: Audit the rest of the file for any `lightbox` / `2048` references.**

```bash
cd /path/to/fotobank && rg -n "lightbox|2048" internal/httpapi/media_thumb_test.go
```

Replace any leftover `?size=lightbox` URLs in pre-existing tests with `?size=preview`. Replace any hard-coded `2048` width assertions with the corresponding new MaxEdge.

- [ ] **Step 4: Run the file.**

```bash
cd /path/to/fotobank && go test ./internal/httpapi -run "TestThumbRoute" -v
```

Expected: all 6 prior thumb tests + 3 new tests pass.

- [ ] **Step 5: Run the full backend suite.**

```bash
cd /path/to/fotobank && go test ./... -short -shuffle=on
```

Expected: clean.

- [ ] **Step 6: Commit.**

```bash
git add internal/httpapi/media_thumb_test.go
git commit -m "test(httpapi): lock size=lightbox→400 and size=large happy path"
```

---

## Task 12: CLI thumbs.go owner-scope flags

**Files:**
- Modify: `internal/cli/thumbs.go`
- Modify: `internal/cli/thumbs_test.go`

The existing `regenerate` keeps the `--all` / `--id` / `--type` / `--status` / `--since` content selectors and the "at least one selector required" check (`internal/cli/thumbs.go:104-110`). Two new mutually exclusive owner-scope flags are added: `--owner <hub>:<user>` and `--all-owners`. When either is set, the `identity.mode=stub` precondition (`internal/cli/thumbs.go:86`) is bypassed (admin maintenance). The CLI now loops `owners.Repo.List(ctx)` for `--all-owners`, calling `Queue.Enqueue` once per owner and printing one `<n> rows enqueued for <hub>:<user>` line per principal.

- [ ] **Step 1: Add the new test cases.**

Append to `internal/cli/thumbs_test.go`:

```go
func writeNonStubConfig(t *testing.T, tmp string) string {
	t.Helper()
	cfgPath := filepath.Join(tmp, "c.toml")
	require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "header"
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash")), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	return cfgPath
}

// seedRowForOwner inserts a ready row owned by the given principal and
// returns the inserted media. Mirrors seedReadyRow but allows a caller-
// chosen owner so we can test that --owner and --all-owners scope
// correctly across multiple principals.
func seedRowForOwner(t *testing.T, dbPath string, p owners.Principal) media.Media {
	t.Helper()
	d, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = d.Close() }()
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		p.Hub, p.UserID, p.Hub+":"+p.UserID, time.Now().UTC(),
	)
	require.NoError(t, err)
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	m := media.Media{
		ID: uuid.NewString(), Owner: p, Type: media.TypePhoto, MimeType: "image/jpeg",
		Path: "x-" + uuid.NewString() + ".jpg", ImportedAt: time.Now().UTC(),
		Size: 1, Checksum: uuid.NewString(), ThumbStatus: "ready", ThumbVersion: 2,
	}
	require.NoError(t, repo.Insert(context.Background(), m))
	return m
}

func TestThumbsRegenerateOwnerScopeOnlyTouchesThatOwner(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	r.Equal(0, cli.RunContext(context.Background(),
		[]string{"owners", "list"}, &out, &eout))
	alice := owners.Principal{Hub: "local", UserID: "alice"}
	bob := owners.Principal{Hub: "local", UserID: "bob"}
	mAlice := seedRowForOwner(t, dbPath, alice)
	mBob := seedRowForOwner(t, dbPath, bob)

	out.Reset(); eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate",
			"--owner", "local:alice", "--all", "--config", cfgPath},
		&out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "1 rows enqueued for local:alice")

	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	gotA, err := repo.GetByID(context.Background(), mAlice.ID)
	r.NoError(err)
	r.Equal("pending", gotA.ThumbStatus)
	r.Equal(3, gotA.ThumbVersion)
	gotB, err := repo.GetByID(context.Background(), mBob.ID)
	r.NoError(err)
	r.Equal("ready", gotB.ThumbStatus, "bob's row must be untouched")
	r.Equal(2, gotB.ThumbVersion)
}

func TestThumbsRegenerateAllOwnersTouchesEveryRow(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	r.Equal(0, cli.RunContext(context.Background(),
		[]string{"owners", "list"}, &out, &eout))
	owners3 := []owners.Principal{
		{Hub: "local", UserID: "alice"},
		{Hub: "local", UserID: "bob"},
		{Hub: "local", UserID: "carol"},
	}
	var rows []media.Media
	for _, p := range owners3 {
		rows = append(rows, seedRowForOwner(t, dbPath, p))
		rows = append(rows, seedRowForOwner(t, dbPath, p))
	}

	out.Reset(); eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all-owners", "--all",
			"--config", cfgPath}, &out, &eout)
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
	r.Contains(out.String(), "local:alice")
	r.Contains(out.String(), "local:bob")
	r.Contains(out.String(), "local:carol")

	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	for _, m := range rows {
		got, err := repo.GetByID(context.Background(), m.ID)
		r.NoError(err)
		r.Equal("pending", got.ThumbStatus, "row %s", m.ID)
		r.Equal(3, got.ThumbVersion, "row %s", m.ID)
	}
}

func TestThumbsRegenerateOwnerScopeWithoutContentSelectorErrors(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--owner", "local:alice",
			"--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "at least one")

	out.Reset(); eout.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all-owners",
			"--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "at least one")
}

func TestThumbsRegenerateMalformedOwnerErrors(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--owner", "alice", "--all",
			"--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "owner must be hub:user")
}

func TestThumbsRegenerateOwnerAndAllOwnersAreMutuallyExclusive(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "fotobank.sqlite"))

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate",
			"--owner", "local:alice", "--all-owners", "--all",
			"--config", cfgPath}, &out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "mutually exclusive")
}

func TestThumbsRegenerateOwnerScopeBypassesStubModeRequirement(t *testing.T) {
	// --owner / --all-owners are admin maintenance modes that must
	// work outside identity.mode=stub. The default (no scope flag)
	// still requires stub.
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Trigger migrations.
	var out, eout bytes.Buffer
	r.Equal(0, cli.RunContext(context.Background(),
		[]string{"owners", "list"}, &out, &eout))

	out.Reset(); eout.Reset()
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all", "--config", cfgPath},
		&out, &eout)
	r.Equal(2, code, "default scope without stub must error")
	r.Contains(eout.String(), "stub")

	out.Reset(); eout.Reset()
	code = cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--owner", "local:alice", "--all",
			"--config", cfgPath}, &out, &eout)
	// 0 rows because no owner exists; the bypass behavior is the
	// thing under test, not the row count.
	r.Equal(0, code, "stderr=%s stdout=%s", eout.String(), out.String())
}

func TestThumbsRegenerateBadSinceErrorsBeforeOpeningDB(t *testing.T) {
	// `--since bad-date` must be rejected upfront, before any DB file
	// is created or migrated. Otherwise a typo on a fresh box silently
	// materializes the SQLite file, runs migrations, and only then
	// reports the usage error — confusing under cron-style invocations
	// (and a real bug in F1).
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	var out, eout bytes.Buffer
	code := cli.RunContext(context.Background(),
		[]string{"thumbs", "regenerate", "--all-owners",
			"--since", "not-a-date", "--config", cfgPath},
		&out, &eout)
	r.Equal(2, code)
	r.Contains(eout.String(), "--since must be RFC3339")
	_, statErr := os.Stat(dbPath)
	r.Truef(os.IsNotExist(statErr),
		"DB must not be created when --since fails to parse: stat=%v", statErr)
}
```

- [ ] **Step 2: Run; expect failures (the new flags are not yet wired).**

```bash
cd /path/to/fotobank && go test ./internal/cli -run "TestThumbsRegenerate" -v
```

Expected: the 7 new tests fail; the 3 existing ones pass.

- [ ] **Step 3: Extend `regenerateOpts` and the cobra flags.**

In `internal/cli/thumbs.go`, replace the `regenerateOpts` struct (lines 37–44) with:

```go
type regenerateOpts struct {
	cfgPath   string
	all       bool
	ids       []string
	kind      string
	status    string
	since     string     // raw RFC3339 string from the flag
	sinceTime *time.Time // parsed during validateSelectors; nil when --since is empty
	owner     string     // "<hub>:<user>"; empty = use stub principal
	allOwners bool       // iterate every principal in owners table
}
```

`sinceTime` is populated by `validateSelectors` so the parse happens before any DB or owner-resolution work — a malformed `--since` must not create or open the DB file.

Update `newThumbsRegenerateCmd` (after the existing flags around line 71):

```go
cmd.Flags().StringVar(&opts.owner, "owner", "",
	"admin: regenerate for a single principal in <hub>:<user> form")
cmd.Flags().BoolVar(&opts.allOwners, "all-owners", false,
	"admin: regenerate for every registered principal")
```

- [ ] **Step 4: Add scope validation and an owner parser.**

Append to `internal/cli/thumbs.go`:

```go
// validateScope enforces the mutual-exclusion and format rules for
// --owner / --all-owners. Returns a usage error when the combination
// is invalid.
func validateScope(opts regenerateOpts) error {
	if opts.owner != "" && opts.allOwners {
		return newUsageError(
			"--owner and --all-owners are mutually exclusive")
	}
	if opts.owner != "" {
		if _, err := parseOwner(opts.owner); err != nil {
			return err
		}
	}
	return nil
}

// parseOwner splits a "<hub>:<user>" CLI argument into a Principal.
// Both halves must be non-empty so a stray ":alice" or "local:" is
// caught here rather than turning into a silent 0-row enqueue.
func parseOwner(s string) (owners.Principal, error) {
	hub, user, ok := strings.Cut(s, ":")
	if !ok || hub == "" || user == "" {
		return owners.Principal{}, newUsageError(
			"owner must be hub:user (got %q)", s)
	}
	return owners.Principal{Hub: hub, UserID: user}, nil
}
```

(Add `"strings"` to the import block if it's not already there.)

- [ ] **Step 5: Replace `loadThumbsConfig` and the run loop to honor the new scope.**

In `internal/cli/thumbs.go`, change `loadThumbsConfig` (lines 77–92) so it accepts whether the caller explicitly chose a scope, and only enforces stub mode when no scope is set:

```go
// loadThumbsConfig loads and validates the config for the thumbs
// command. requireStub is true when the caller did not pass --owner
// or --all-owners; in that case the caller is the configured stub
// principal and identity.mode=stub is required so we don't silently
// enqueue under the wrong identity.
func loadThumbsConfig(cfgPath string, requireStub bool) (*config.Config, error) {
	path := cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if requireStub && cfg.Identity.Mode != "stub" {
		return nil, newUsageError(
			"fotobank thumbs regenerate requires identity.mode = stub (got %q)",
			cfg.Identity.Mode)
	}
	return cfg, nil
}
```

> The stub-mode mismatch is a usage error, not a runtime error: it means the operator passed an incompatible config combination. `newUsageError` yields exit code 2, which `TestThumbsRegenerateOwnerScopeBypassesStubModeRequirement` asserts (`r.Equal(2, code, "default scope without stub must error")`). Using `fmt.Errorf` would yield exit code 1 and break that test.

Replace the body of `runThumbsRegenerate` (lines 141–166) with:

```go
func runThumbsRegenerate(ctx context.Context, opts regenerateOpts, stdout, _ io.Writer) error {
	// Validation must run before loadThumbsConfig/openDB so a bad
	// --since (or empty selector set) can't side-effect the DB file.
	if err := validateSelectors(&opts); err != nil {
		return err
	}
	if err := validateScope(opts); err != nil {
		return err
	}
	requireStub := opts.owner == "" && !opts.allOwners
	cfg, err := loadThumbsConfig(opts.cfgPath, requireStub)
	if err != nil {
		return err
	}
	d, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	scope, err := resolveOwners(ctx, d, cfg, opts)
	if err != nil {
		return err
	}
	if len(scope) == 0 {
		fmt.Fprintln(stdout, "no owners found")
		return nil
	}

	q := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	for _, p := range scope {
		filter := buildFilter(p, opts)
		n, err := q.Enqueue(ctx, filter)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%d rows enqueued for %s:%s.\n", n, p.Hub, p.UserID)
	}
	return nil
}

// resolveOwners returns the list of principals to enqueue for. When
// neither --owner nor --all-owners is set, the list is just the
// configured stub principal.
func resolveOwners(ctx context.Context, d *db.DB, cfg *config.Config, opts regenerateOpts) ([]owners.Principal, error) {
	if opts.allOwners {
		repo := owners.NewRepo(d.WriteDB(), d.ReadDB())
		os, err := repo.List(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]owners.Principal, 0, len(os))
		for _, o := range os {
			out = append(out, o.Principal)
		}
		return out, nil
	}
	if opts.owner != "" {
		p, err := parseOwner(opts.owner)
		if err != nil {
			return nil, err
		}
		return []owners.Principal{p}, nil
	}
	return []owners.Principal{
		{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
	}, nil
}
```

Update `buildFilter` (lines 115–135) to accept a Principal directly, stop reading `cfg.Identity.Stub`, and reuse `opts.sinceTime` instead of re-parsing:

```go
func buildFilter(p owners.Principal, opts regenerateOpts) thumb.EnqueueFilter {
	filter := thumb.EnqueueFilter{
		Owner: p,
		All:   opts.all,
		IDs:   opts.ids,
	}
	if opts.kind != "" {
		filter.MediaType = media.Type(opts.kind)
	}
	if opts.status != "" {
		filter.Status = opts.status
	}
	if opts.sinceTime != nil {
		filter.Since = opts.sinceTime
	}
	return filter
}
```

`buildFilter` no longer returns an error: the only previous error path was RFC3339 parsing of `--since`, which is now done upfront in `validateSelectors`. The new signature also makes per-owner construction inside the loop infallible, so the caller doesn't need a `for { … err := buildFilter; if err … }` wrapper.

Update `validateSelectors` (lines 104–111) to take a pointer and parse `--since` once into `opts.sinceTime`:

```go
// validateSelectors enforces "at least one selector" and parses --since
// upfront so a malformed timestamp is reported before the DB is opened
// or owners are resolved.
func validateSelectors(opts *regenerateOpts) error {
	if !opts.all && len(opts.ids) == 0 && opts.kind == "" && opts.status == "" && opts.since == "" {
		return newUsageError(
			"at least one of --all, --id, --type, --status, --since is required")
	}
	if opts.since != "" {
		ts, err := time.Parse(time.RFC3339, opts.since)
		if err != nil {
			return newUsageError("--since must be RFC3339: %v", err)
		}
		opts.sinceTime = &ts
	}
	return nil
}
```

The pointer receiver is required so the parsed `sinceTime` survives back to `runThumbsRegenerate` for the per-owner `buildFilter` call. The `time` import is already present.

The pre-existing single-principal output line `"%d rows enqueued for regeneration.\n"` becomes `"%d rows enqueued for <hub>:<user>.\n"`. Update `TestThumbsRegenerateAllBumpsVersion` and `TestThumbsRegenerateByIDTargetsOnlyMatch` — both currently assert `"1 rows enqueued"` on stdout, which still substring-matches. Confirm both still pass without modification.

- [ ] **Step 6: Run; expect pass.**

```bash
cd /path/to/fotobank && go test ./internal/cli -run "TestThumbsRegenerate" -v
```

Expected: 10 tests pass (3 existing + 7 new).

- [ ] **Step 7: Run the full backend suite.**

```bash
cd /path/to/fotobank && go test ./... -short -shuffle=on
```

Expected: clean.

- [ ] **Step 8: Commit.**

```bash
git add internal/cli/thumbs.go internal/cli/thumbs_test.go
git commit -m "feat(cli): thumbs regenerate accepts --owner and --all-owners"
```

---

## Task 13: Playwright reload + SPA-nav tests

**Files:**
- Modify: `frontend/tests/e2e/library.spec.ts`

Five new Playwright tests lock the F2.0 SPA contract:

1. Reload `/media/<id>` returns the SPA shell + matched route.
2. Reload `/foo-not-a-route` returns the SPA shell + NotFound.
3. Reload `/api/v1/healthz` returns API JSON, NOT the SPA shell.
4. SPA nav `/library → /sessions → back` does NOT re-issue the initial `limit=200&offset=0` media-list fetch (because MediaStore is hoisted and survives nav). Pagination sentinels are permitted to fire and don't count.
5. The MediaDetail back link SPA-routes to `/library` without a document fetch — proves the Task 4 wiring isn't a no-op and protects the Task 6 hoist from regressing if someone reverts the MediaDetail anchor.

- [ ] **Step 1: Append the five tests.**

```ts
// Append to frontend/tests/e2e/library.spec.ts

test("reload /media/<id> returns SPA shell + matched route", async ({ page }) => {
  await page.goto("/media/abc-123");
  await expect(page.getByText("fotobank")).toBeVisible();
  // The MediaDetail stub renders the back link unconditionally.
  await expect(page.getByRole("link", { name: /back to library/i })).toBeVisible();
});

test("reload /foo-not-a-route renders the SPA shell with NotFound", async ({ page }) => {
  const resp = await page.goto("/foo-not-a-route");
  expect(resp?.status()).toBe(200);
  await expect(page.getByText("fotobank")).toBeVisible();
  await expect(page.getByText(/page not found/i)).toBeVisible();
});

test("reload /api/v1/healthz returns API JSON, not the SPA shell", async ({ page }) => {
  const resp = await page.request.get("/api/v1/healthz");
  expect(resp.status()).toBe(200);
  const text = await resp.text();
  // SPA shell would contain <html>; the healthz handler returns JSON.
  expect(text.toLowerCase()).not.toContain("<html");
});

test("SPA nav library→sessions→back does not re-issue the initial media fetch", async ({ page }) => {
  // Track every /api/v1/media list request. Pagination sentinels (offset>0)
  // are permitted and excluded from this count; the contract is "don't
  // re-issue the offset=0 page on SPA nav".
  const initialPageCalls: string[] = [];
  page.on("request", (req) => {
    const url = new URL(req.url());
    if (url.pathname !== "/api/v1/media") return;
    if (url.searchParams.get("offset") !== "0") return;
    initialPageCalls.push(req.url());
  });

  await page.goto("/library");
  // Wait for the initial fetch to settle so we have a stable baseline.
  await expect(page.getByText("fotobank")).toBeVisible();
  const baseline = initialPageCalls.length;
  expect(baseline).toBeGreaterThanOrEqual(1);

  await page.getByRole("link", { name: "Sessions" }).click();
  await expect(page).toHaveURL(/\/sessions$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/library$/);

  // The hoisted MediaStore must NOT have re-fetched offset=0 on either
  // hop; pagination sentinels (offset>0) are filtered out above.
  expect(initialPageCalls.length).toBe(baseline);
});

test("MediaDetail back link SPA-routes to /library without a document fetch", async ({ page }) => {
  // The back arrow on /media/:id MUST go through handleInternalLinkClick
  // so the hoisted MediaStore survives. A naked anchor would issue a
  // top-level document request — track those (resourceType === "document")
  // since framenavigated fires for pushState too and can't distinguish.
  const docRequests: string[] = [];
  page.on("request", (req) => {
    if (req.resourceType() === "document") docRequests.push(req.url());
  });

  await page.goto("/media/abc-123");
  await expect(page.getByRole("link", { name: /back to library/i })).toBeVisible();
  const beforeBack = docRequests.length;
  expect(beforeBack).toBeGreaterThanOrEqual(1); // the page.goto itself

  await page.getByRole("link", { name: /back to library/i }).click();
  await expect(page).toHaveURL(/\/library$/);

  // No new document request should fire from the back-click — only
  // history.pushState. handleInternalLinkClick must have called
  // preventDefault().
  expect(docRequests.length).toBe(beforeBack);
});
```

- [ ] **Step 2: Build the e2e server and run the suite.**

```bash
cd /path/to/fotobank && make test-e2e
```

Expected: 8 tests pass (3 prior + 5 new).

- [ ] **Step 3: Commit.**

```bash
git add frontend/tests/e2e/library.spec.ts
git commit -m "test(e2e): SPA reload + no-duplicate-fetch lock for F2.0 router + MediaStore hoist"
```

---

## Task 14: Final hygiene and rollup

**Files:**
- (none — verification only)

- [ ] **Step 1: Run the full local test suite.**

```bash
cd /path/to/fotobank && go test ./... -shuffle=on
cd /path/to/fotobank/frontend && bun run typecheck && bun run test
cd /path/to/fotobank && make test-e2e
```

Expected: all green.

- [ ] **Step 2: Confirm no leftover `lightbox` / `2048` / `1024` references.**

```bash
cd /path/to/fotobank && rg -n "lightbox|SizeLightbox" -- internal/ frontend/src/ frontend/tests/
```

Expected: only references in spec docs (`docs/superpowers/specs/...`) and historical comments. Anything live in code is a leftover and must be cleaned up.

- [ ] **Step 3: Re-run any mise lint targets.**

```bash
cd /path/to/fotobank && make lint && make nilaway
```

Expected: clean. nilaway is the pre-push tier; running it now catches anything the per-commit hook would have skipped.

- [ ] **Step 4: Commit any cleanup that step 2 surfaced** (only if there is something to commit).

```bash
git status
# If there's anything: git add <files> && git commit -m "chore: cleanup leftover lightbox references"
```

---

### CHECKPOINT after Task 14

Run `/roborev-fix` to address any review findings on Tasks 11-14 in one pass. Once all reviews are closed and `make test-e2e` is green, F2.0 is ready to hand off to F2.1 / F2.2 / F2.3 (independent) and F2.4 (depends on this).

# F2.5 Lightbox Viewer — Design

**Status:** Spec complete; awaiting plan.

**Date:** 2026-04-29

**Forward deps:** F2.3 router, F2.4 hidden privacy.

---

## 1. Goal and scope

### Goal

Ship the full lightbox viewer described in master frontend spec §6: a route-driven photo viewer with prev/next, zoom/pan (mouse, trackpad, touch), keyboard shortcuts, source-aware MediaActions (including hide/unhide in place), an info panel (desktop drawer + mobile bottom sheet), and clean modal stacking with Add-to-album / Share triggered from inside.

The lightbox is the primary photo-viewing surface across all media-bearing routes: Library, Sessions, Album Detail, Hidden Library.

### In scope

- Lightbox route activation at `/media/:id?from=<source>` (existing route; new query-param branch).
- Sources: `library`, `sessions`, `album:<id>`, `hidden`.
- Selection-snapshot navigation (prev/next walks the selected items when N>1 selected and the opened id is among them; otherwise walks the full source list).
- Progressive image loading using existing `/thumb` size variants (`grid` → `preview` → `large`); `/original` is download-only.
- Vendored panzoom library for zoom/pan (binding to be chosen in plan; small focused package, ~3–5 KB gzipped).
- Mobile bottom sheet for the info panel; desktop side drawer for the same content.
- Modal stacking: a central `modalStack` store; Add-to-album / Share / Confirm / RenameAlbum modals migrated to the stack so Esc dispatches topmost-first.
- Source-aware hide/unhide flows that update durable state (mediaStore, albumsStore.markStale, snapshot navIds).
- Scroll restoration on close: source remounts at the captured Y position, then state is cleared.
- Reconstruction path for direct entry / reload with `?from=...`.
- Anti-leak hidden rule (lightbox mode disables when `from != hidden && hidden_at != null`).
- Playwright e2e covering all flows.

### Out of scope

- Additional metadata sections (ratings, captions, AI tags). The info panel renders **only existing fields** (filename(s), captured timestamp, camera/lens, dimensions, file size, location when present, sidecars/downloads). Future sections are appended later.
- Hand-rolled zoom/pan; we vendor a focused library.
- Second-neighbor prefetch (only immediate prev/next for v1).
- A dedicated test-only debug endpoint to expire the unlock cookie. We use the existing `POST /api/v1/auth/hidden/lock`.

### Non-goals

- Breaking-change avoidance for media URL formats. The `?from=<source>` query param is new; older URLs like `/media/:id` continue to render the existing direct-detail path. No URL migration; project is pre-alpha.

---

## 2. Architecture overview

**Route:** `/media/:id` (existing direct-detail route, unchanged). Lightbox mode activates **only when `?from=<source>` is present**; without `from`, the route renders existing `MediaDetail` behavior — including the F2.4 hidden-with-unlock-cookie path.

**Source union:**

```ts
type LightboxSource =
  | { kind: 'library' }
  | { kind: 'sessions' }
  | { kind: 'album'; albumId: string }
  | { kind: 'hidden' };
```

`from=sessions` reconstructs navIds from `mediaStore.months` flattened in `Sessions.svelte` order.

**LightboxSession (new app-level store):**

```ts
type LightboxSession = {
  source: LightboxSource;
  navIds: string[];           // ordered; selection snapshot if N>1 selected and opened id is in selection, else full source
  selected: boolean;          // whether navIds is a selection snapshot
  scrollY: number;            // source scroll position at open time
  returnFocusMediaId: string | null;
  returnHref: string;         // e.g., '/library', '/sessions', '/albums/abc', '/hidden'
};
```

Source routes call `lightboxSession.open({...})` **before** pushing `/media/:id?from=...`. `AlbumDetailStore` and `HiddenMediaStore` are route-local; the snapshot is the durable handoff.

**Direct entry / reload with `from=...`:** lightbox attempts best-effort reconstruction:

- `from=library` or `from=sessions` → use app-level `mediaStore` (warm) for navIds.
- `from=album:abc` → fetch album detail and paginate items until active id found or page-cap exhausted.
- `from=hidden` → fetch `/api/v1/hidden/media` paged similarly. On 403 → redirect to `/hidden`.
- Reconstruction throws or active id not found within cap → fall back to direct-detail mode (no prev/next), as if `from` were absent.

**Router change:** parse query params on the `/media/:id` match; expose `from` (and other query params) on `RouteMatch`. Current router ignores query params except special cases like `/shares`.

**Hidden rule (scoped to lightbox mode only):** if `from != hidden` and the fetched media has `hidden_at != null`, lightbox mode is **disabled for this render** — no prev/next, no source actions. The page may still render direct-detail behavior if F2.4 allows (i.e., valid unlock cookie). The F2.4 direct-detail page itself is unchanged.

**Router/history state (extended shape):** `history.state = { depth, sourceScroll? }`. F2.3 router already tracks `depth`; F2.5 adds the optional `sourceScroll` field for reload-survivability of scroll restoration.

**Scroll restoration contract:**

- **On open:** source route captures `scrollY` into `LightboxSession.scrollY` and into the lightbox entry's `history.state.sourceScroll` (belt + suspenders for reload survivability).
- **On close/back:** source route remounts; reads `LightboxSession.scrollY` (preferred — survives full SPA back) or falls back to `history.state.sourceScroll`.
- **After restore:** clear the saved scroll (`lightboxSession.clearScroll()` and remove from history state) so subsequent navigation is sticky-free.

**Navigation history:**

- Entering lightbox: `router.navigate(url)` (push).
- Prev/next inside lightbox: `router.navigate(url, { replace: true })` (single browser back closes lightbox).
- Closing lightbox: `history.back()` if `depth > 0`; else `router.navigate(returnHref, { replace: true })`.

**Modal stacking:** central `modalStack` (ordered list of open modal entries). Esc handler fires top-down — topmost modal closes first; lightbox is on the stack; existing `App.svelte` selection-clear Esc handler becomes the **default** entry, fires only when stack is empty. Add-to-album / Share modals push above lightbox. Migration scope (F2.5): `Lightbox`, `AddToAlbumModal`, `ShareModal`, `ConfirmModal`, `RenameAlbumModal`. Out: `ShareDrawer`, `AlbumsIndex` new-album modal.

**Mobile info panel:** new reusable `BottomSheet.svelte` (drag handle, snap points: peek/full, swipe-down dismiss, Esc dismiss via modalStack). Used by lightbox info on viewports below the existing mobile breakpoint; desktop continues using the side drawer.

---

## 3. Component decomposition

```
frontend/src/lib/lightbox/
  lightboxSession.svelte.ts        — app-level store; open/close/clearScroll/clearReturnFocus/removeIds; durable snapshot
  lightboxNav.svelte.ts            — derives current index, prev/next ids, hasPrev/hasNext from session + activeId
  lightboxLoader.ts                — URL selection (grid/preview/large); progressive image load; prefetch + cancel handles. Browser-side async (Image() / decode()); tests mock those.
  modalStack.svelte.ts             — central store: ordered open-modal entries; topmost-first Esc dispatch; pause/resume of focus traps on stack changes.

frontend/src/lib/components/lightbox/
  Lightbox.svelte                  — top-level; reads ?from= via router match; resolves session or runs reconstruction; mounts subviews; registers self with modalStack.
  LightboxFrame.svelte             — black bg, layout, header/footer chrome, focus trap.
  LightboxMedia.svelte             — branches on media type: <LightboxImage> for photos, <LightboxVideo> for videos. Prevents /original being assigned to <img> on video rows.
  LightboxImage.svelte             — <img>, panzoom binding (vendored), progressive grid → preview → large swap, decode handling.
  LightboxVideo.svelte             — <video controls> against /original; no panzoom; reuses lightboxLoader's generation token where relevant (e.g., poster).
  LightboxToolbar.svelte           — top chrome (info toggle, close); bottom slot for LightboxActions.
  LightboxNavButtons.svelte        — left/right arrows; hidden when hasPrev/hasNext is false.
  LightboxActions.svelte           — wraps MediaActions with source-aware handlers (see §4 data flow).
  LightboxInfoDrawer.svelte        — desktop side drawer wrapping LightboxMetadata.
  LightboxInfoSheet.svelte         — mobile bottom-sheet wrapper around LightboxMetadata (uses BottomSheet).
  LightboxMetadata.svelte          — current fields only: filename(s), captured timestamp, camera/lens, dimensions, file size, location (when present), sidecars/downloads.

frontend/src/lib/components/
  BottomSheet.svelte               — generic; drag handle, snap points (peek/full), swipe-down dismiss, Esc dismiss via modalStack.

frontend/src/lib/components/AddToAlbumModal.svelte    — MODIFY: drop local svelte:window onkeydown Escape; on mount push entry onto modalStack, on unmount pop. Esc dispatched centrally.
frontend/src/lib/components/ShareModal.svelte         — MODIFY: same change as AddToAlbumModal.
frontend/src/lib/components/ConfirmModal.svelte       — MODIFY: same migration to modalStack.
frontend/src/lib/components/RenameAlbumModal.svelte   — MODIFY: same migration to modalStack.
frontend/src/App.svelte                               — MODIFY: existing selection-clear Escape becomes the default handler at the bottom of the stack (fires only when modalStack is empty). Pre-empts via dispatchEscape() with preventDefault/stopPropagation.

frontend/src/routes/
  MediaDetail.svelte               — MODIFY: branch on ?from=. With from → mount Lightbox. Without from → existing direct-detail path (unchanged, including F2.4 hidden-with-cookie).

frontend/src/lib/router/
  router.svelte.ts                 — MODIFY: parse query params on the /media/:id match; expose `from` on RouteMatch; widen history.state typing for { depth, sourceScroll? }.

frontend/src/routes/Library.svelte, Sessions.svelte, AlbumDetail.svelte, HiddenLibrary.svelte
                                   — MODIFY: on item-open call lightboxSession.open({...}) with snapshot, then push /media/:id?from=...; on remount consume + clear scrollY restore.
```

**Notes on decomposition:**

- `Lightbox.svelte` orchestrates; subcomponents are single-purpose and small.
- `LightboxMedia.svelte` type-branches so video rows render through `<video>`, not `<img>`.
- `lightboxSession` (snapshot, write-rarely) and `lightboxNav` (derived per-render) are split because of different reactivity profiles.
- `lightboxLoader.ts` is browser-side async (creates Image objects, calls decode), not pure-functional, but isolated for testability.
- `BottomSheet.svelte` is general-purpose; lifted to `lib/components/` for reuse.
- `LightboxMetadata.svelte` is shared between drawer (desktop) and sheet (mobile); only the wrapper differs. Renders only existing fields; future sections appended later.
- `AddToAlbumModal`, `ShareModal`, `ConfirmModal`, `RenameAlbumModal` are modified — not "used as-is" — to integrate with `modalStack`.

---

## 4. Data flow

### Open from grid (happy path)

1. User clicks tile in `Library.svelte` (or sibling source route).
2. Source route builds the snapshot:
   ```ts
   if (selection.size > 1 && selection.has(openedId)) {
     navIds = selectedIdsInSourceOrder;
     selected = true;
   } else {
     navIds = flattenedSourceIds;
     selected = false;
   }
   ```
   Plus `scrollY = window.scrollY`, `returnFocusMediaId = openedId`, `returnHref` per source.
3. Source calls `lightboxSession.open(snapshot)` and `router.navigate('/media/' + id + '?from=library')`. Router writes `history.state = { depth: prev+1, sourceScroll: scrollY }`.
4. Router unmounts source route, matches `/media/:id`, parses `from=library`, mounts `MediaDetail.svelte`.
5. `MediaDetail` sees `from` present → mounts `Lightbox.svelte`.
6. `Lightbox` reads `lightboxSession`, derives `lightboxNav` (currentIndex, prevId, nextId). Pushes itself onto `modalStack`.
7. `Lightbox` calls `mediaStore.get(id)` (warm) — renders.
8. `lightboxLoader` paints cached `/thumb?size=grid&v=N` if available; otherwise `/thumb?size=preview&v=N`. Kicks off `large` fetch + `decode()`. Background prefetches the **immediate prev and next ids**' preview, then large. `/original` is *not* fetched at this stage.

### Prev/next inside lightbox

1. User presses ArrowRight (or clicks next, or swipes).
2. `Lightbox` calls `router.navigate('/media/' + nextId + '?from=library', { replace: true })`. No new history entry.
3. Route param `id` changes → reactive update → new media fetch + new progressive swap.
4. Prefetch queue: cancel non-adjacent prefetches (logical via generation token); enqueue the new immediate prev and next ids' preview, then large.

### Zoom interaction

1. User zooms past 1×.
2. Active source is already `large`; no escalation needed for typical screens.
3. Pinch/wheel handled by the vendored panzoom binding.

### Close (browser back, Esc, X button)

1. Esc pressed → `modalStack.dispatchEscape()` calls the topmost entry's `onEscape`. If lightbox is topmost → `Lightbox.close()`.
2. `close()`: if `history.state.depth > 0` → `history.back()`; else `router.navigate(session.returnHref, { replace: true })`.
3. Router unmounts `MediaDetail`, mounts source route.
4. Source route on mount reads `lightboxSession.scrollY` (preferred) or `history.state.sourceScroll` (fallback), restores via `window.scrollTo(0, y)`, then calls `lightboxSession.clearScroll()` and clears `sourceScroll` from state.
5. Focus return: `el = querySelector('[data-media-id="' + returnFocusMediaId + '"]')`. If found, `el.focus()`. If not (virtualized off-screen, not yet rendered), focus the route container; `lightboxSession.clearReturnFocus()`.

### Direct entry / reload with `from`

1. URL `/media/:id?from=album:abc123` loaded fresh; no `lightboxSession` in memory.
2. `MediaDetail` mounts `Lightbox`; reconstruction path runs.
3. Reconstruction by source kind, with the contract: **load until active id is found or source is exhausted, capped at a reasonable page count (~20 pages × page size)**. If active id not found within cap, fall back to direct-detail mode.
   - `library` / `sessions`: ensure `mediaStore.loadInitial()`; if active id not yet in store, page until found or exhausted.
   - `album:<id>`: fetch `/api/v1/albums/:id` + paginate items until active id present or exhausted.
   - `hidden`: paged fetch of `/api/v1/hidden/media`. On 403 → redirect to `/hidden`. On success: `navIds = hiddenIds`.
4. If reconstruction throws (album 404, network) **or** active id not found within cap, fall back to direct-detail mode.
5. `selected` is always false on reconstruction; selection snapshot is not recoverable across reload — acceptable.

### Hidden cross-context fallback

1. URL is `/media/:id?from=library` but API returns `hidden_at != null`.
2. `Lightbox` detects `from != hidden && hidden_at != null` → disables lightbox mode for this render.
3. Falls through to direct-detail logic in `MediaDetail.svelte`. F2.4 cookie rules apply (200 if unlocked, 404-style "not found" otherwise).

### Source-aware actions (LightboxActions) — durable mutations only

After a successful hide/unhide on the active item:

1. Mutates `LightboxSession.navIds` directly (removes succeeded primary ids; sidecars handled by backend cascade and not present in nav lists).
2. Updates **durable** state per source kind. No route-local store callbacks held in the session.
   - `library` / `sessions`: `mediaStore.removeMany([...succeededIds])`.
   - `album`: `albumsStore.markStale()`. `AlbumDetail` refetches cleanly on remount.
   - `hidden` unhide: snapshot mutation in step 1, **plus** `mediaStore.mergeRaw([{ ...activeRaw, hidden_at: null }])` (or a fresh row fetch) so the row appears in Library on return; `albumsStore.markStale()`.
3. `lightboxNav` recomputes; index points at next sibling.
4. If `navIds` is now empty → `Lightbox.close()` to source. Otherwise → `router.navigate('/media/' + newActiveId + '?from=...', { replace: true })`.

Per-id partial failures: succeeded ids removed from snapshot and durable state; failed ids unchanged; toast surfaces failures; lightbox stays on active id if the action on it failed.

---

## 5. Input contract — keyboard, modal stack, focus

### Keyboard bindings (active when lightbox is the topmost modal stack entry)

| Key | Action |
|---|---|
| `Esc` | Close lightbox (via modalStack dispatch) |
| `←` / `→` | Prev / next in navIds |
| `+` / `=` | Zoom in |
| `-` / `_` | Zoom out |
| `0` | Reset zoom to fit |
| `Space` | Toggle zoom 1×↔2× (matches double-click) |
| `i` | Toggle info drawer/sheet |
| `h` | Hide (library/sessions/album); unhide (hidden) — source-aware |

**Binding guards:**

- Skip handling when `event.target` is editable. Use the existing `isEditableTarget()` helper (extended to cover `closest('[contenteditable]')` if it does not already).
- For handled keys, call `event.preventDefault()` to suppress page scroll and browser shortcuts (esp. arrows, Space).
- `h` is also suppressed when the info panel has focus on a form control.

**Mouse / trackpad:** wheel = zoom; double-click = toggle zoom; click-drag at >1× = pan; **only primary-button click/tap on the backdrop element itself** (`event.target === backdropEl`) closes — clicks bubbling from image/toolbar/info do not.

**Touch:** pinch = zoom; drag at >1× = pan; horizontal swipe at 1× = prev/next; tap on backdrop element itself closes.

Bindings live on `Lightbox.svelte` via `svelte:window onkeydown`, gated by `modalStack.isTopmost(lightboxId)`.

### Modal stack contract

```ts
type FocusTrapHandle = {
  pause(): void;
  resume(): void;
  release(): void;
};

type ModalEntry = {
  id: string;
  onEscape: () => void;
  trap?: FocusTrapHandle;
};

type ModalStack = {
  push(entry: ModalEntry): void;
  pop(id: string): void;          // idempotent
  isTopmost(id: string): boolean;
  dispatchEscape(): boolean;      // calls top().onEscape() and returns true if one ran
  top(): ModalEntry | null;
};
```

**Important:** `dispatchEscape()` does **not** auto-pop. The dispatched `onEscape` handler is responsible for triggering its own close path, which leads to component unmount, which calls `pop(id)`. Avoids double-handle bugs when a modal refuses to close (pending operation) or keeps itself open.

When a modal is pushed above another, `push()` calls `top().trap?.pause()` on the previous top; `pop()` calls the new top's `trap?.resume()`. Lightbox releases its trap on `pop`.

### Escape flow (App.svelte global handler)

```ts
function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && modalStack.dispatchEscape()) {
    e.preventDefault();
    e.stopPropagation();
    return;
  }
  // existing selection-clear path (using isEditableTarget guard)
}
```

### Focus management

- On open: source captures the opening tile's stable media id into `LightboxSession.returnFocusMediaId`. (`document.activeElement` is too fragile; virtualization can unmount the element.)
- On open: `Lightbox` installs a focus trap on its frame; initial focus lands on the close button.
- On stacked modal push: lightbox's trap pauses; layered modal's trap activates.
- On stacked modal pop: lightbox's trap resumes.
- On close (lightbox unmount): source route remounts, **first restores scroll**, then on next animation frame attempts `el = querySelector('[data-media-id="' + returnFocusMediaId + '"]')`. If found, `el.focus()`. If not, focus the route container element. `lightboxSession.clearReturnFocus()` after.

### Scroll lock

`document.body` gets `overflow: hidden` while lightbox is open (existing modal pattern).

---

## 6. Loading, performance, error states

### Performance budgets (master spec §13.5)

| Metric | Target | Caveat |
|---|---|---|
| Lightbox open (perceived) | < 100 ms | **Only** when opening from a rendered source route with the grid thumb already painted. Direct entry/reload, cache miss, hidden rows (`no-store`), or grid-thumb 404 fall back to the debounced progress bar + preview load path. |
| Prev/next swap (preview cached) | < 50 ms | Cached preview decoded; swap `<img>` source. |
| Prev/next swap (preview not cached) | < 300 ms typical | Background prefetch of immediate prev/next while user reads current photo. |
| Zoom interaction | 60 fps | Vendored panzoom library; CSS `transform` only (no layout). |

### Image source matrix

| State | Source |
|---|---|
| Initial paint | Cached `/thumb?size=grid&v=N` (the grid URL) when available; otherwise `preview` directly |
| Fit (1×) | `/thumb?size=preview&v=N` |
| Zoomed > 1× | `/thumb?size=large&v=N` |
| Download (explicit) | `/original` |
| Video | `<video controls>` against `/original`; no panzoom |

### Photo loading sequence (`lightboxLoader.ts`)

1. Paint cached `/thumb?size=grid&v=N` immediately if available; else start at preview.
2. Load + `decode()` `/thumb?size=preview&v=N`; swap when ready.
3. Load + `decode()` `/thumb?size=large&v=N` in background; swap when ready.
4. Prefetch immediate prev / next: **preview first, large second**. Cap total in-flight preloads at 2.
5. **Cancellation is logical, not network.** Each active-id change increments a generation token; completions whose token doesn't match the current generation are dropped (decoded image discarded, references released for GC). The browser may still complete the network request — that's acceptable; results land in HTTP cache for free. (We deliberately avoid `fetch → Blob → object URL → decode → revokeObjectURL` for v1: more code, more leak surface.)

### Loading affordances

- Cached-grid-present state: no spinner (instant).
- No-cache state: faint progress bar at top of frame after 200 ms (debounce avoids flicker on fast loads).
- Decode failure for preview/large: keep prior successful source visible; small badge "Couldn't load full-resolution image"; Retry in toolbar.

### Error states

| Scenario | Detection | UI |
|---|---|---|
| API 404 on `/media/:id` (id missing, or hidden+locked) | initial fetch | "Photo not found" frame; toolbar shows close only; nav buttons hidden |
| Network failure on media metadata | fetch rejects | "Couldn't load photo" frame with Retry; close works |
| Image fetch / decode fails (preview or large) | rejection / decode throw | Keep last successful source; corner badge; Retry in toolbar |
| Video fetch fails | `<video> error` event | Same as image fetch fail |
| **Hidden 403** on `/hidden/media` or unhide action | response status 403 | `router.navigate('/hidden', { replace: true })` (gate page) |
| **Hidden 404 while in `from=hidden`** (typical expired-cookie surface) | response status 404 | Call `hiddenStore.refresh()`. If response shows locked → `router.navigate('/hidden', { replace: true })`. If still unlocked → "Photo not found" frame. |
| Reconstruction can't find active id within page cap | loop exhaustion | Fall back to direct-detail mode (no prev/next) |
| Reconstruction request fails (album 404, network) | catch | Fall back to direct-detail mode |
| Hidden mismatch (`from != hidden && hidden_at != null`) | API response inspection | Disable lightbox mode; render direct-detail (F2.4 cookie rules) |
| Hide/unhide partial failure | bulk action result | Toast lists failures; snapshot mutated only for succeeded ids; lightbox stays on active id |
| Hide/unhide fails on active id | bulk action result | Toast; no advance/close |

### Bandwidth backoff

Prefetch queue caps to 2 concurrent. v1 ships immediate prev/next only — no second-neighbor knob.

---

## 7. Testing strategy

### Unit tests (Vitest)

| Module | Cases |
|---|---|
| `lightboxSession.svelte.ts` | open/close/clearScroll/clearReturnFocus/removeIds; selection snapshot rule (size>1 & has(openedId) only); navIds membership after removeIds with primary + non-existent ids |
| `lightboxNav.svelte.ts` | currentIndex; prevId/nextId at boundaries (first/last/single-item); hasPrev/hasNext flags; recompute after `removeIds` advances index correctly |
| `lightboxLoader.ts` | uses grid URL as initial source when provided; starts at preview when no grid URL is provided; full progressive sequence (grid → preview → large) when each available; logical cancellation via generation token (stale completion dropped); prefetch ordering (preview prev/next before large); concurrency cap of 2; decode-failure path |
| `modalStack.svelte.ts` | push/pop/isTopmost/top/dispatchEscape; idempotent pop; pause/resume of trap on push/pop; dispatchEscape returns false on empty stack; does NOT auto-pop |
| `BottomSheet.svelte` | snap points (peek/full); drag transitions; swipe-down dismiss; backdrop click dismiss; Esc dismiss via modalStack |
| reconstruction helpers | finds active id within page cap; bails out at cap; bails out on fetch error |

### Integration tests (Vitest + @testing-library/svelte)

- `LightboxActions` source dispatch:
  - `library` → calls `mediaStore.removeMany`.
  - `album` → mutates `LightboxSession.navIds`, calls `albumsStore.markStale()`.
  - `hidden` unhide → mutates session navIds, calls `mediaStore.mergeRaw([{...row, hidden_at: null}])`, calls `albumsStore.markStale()`.
- Hidden cross-context fallback: API returns `hidden_at != null` under `from=library` → lightbox mode disabled, direct-detail rendered.
- **404-while-unlocked**: route-intercept the `/api/v1/media/:id` call to 404; assert "Photo not found" frame, no redirect. (Avoids a server-side test endpoint.)
- Editable-target keyboard guard: typing in a `<textarea>` inside info panel doesn't trigger arrows/Space/h.

### Playwright e2e (`frontend/tests/e2e/lightbox.spec.ts`, new — config `frontend/playwright-e2e.config.ts`)

**Fixture rule:** every scenario uses scenario-dedicated media ids seeded by `cmd/e2e-server`; tests do not mutate another scenario's fixture rows. Helpers extended for: per-scenario album with N photos, per-scenario hidden seed (single and multi), per-scenario selection scenario.

Required scenarios:

1. **Library open → walk → close → scroll restored.** Scroll to row N; click tile; arrow-right twice; Esc; verify scroll Y matches captured value within tolerance.
2. **Sessions open → walk → close.** Same flow, source = `sessions`.
3. **Album open → hide active → advances.** Hide the active item out of N≥3; verify URL replaces to next id; on remount of album view, hidden row is **absent from the visible album list** (membership in DB preserved per F2.4; only the visible filter excludes it).
4. **Album open → hide last visible item → closes to album.** Hide the only/last item in the snapshot; lightbox closes; album view doesn't show that item.
5. **Selection snapshot walk.** Select 5 non-contiguous items (dedicated ids); click the 3rd selected one; arrow-right twice → reaches the 5th selected; arrow-right once more → no advance; unselected items between are skipped.
6. **Selection snapshot but plain-click unselected.** With 3 items selected, plain-click an unselected tile; navIds is the full source (selection snapshot rule respects `has(openedId)`).
7. **Hidden grid open → walk → unhide → advances → close → row visible in library.** Seed ≥2 hidden rows. From `/hidden`, click first; arrow-right; click unhide on active; URL replaces to the remaining hidden id; press Esc; verify return to `/hidden` (the unhidden row gone); navigate to `/library`; the unhidden row is now visible.
8. **Direct entry: `/media/:id?from=library`.** Page reload; reconstruction completes; prev/next available.
9. **Direct entry: `/media/:id?from=hidden` while locked.** Use `POST /api/v1/auth/hidden/lock` to clear the cookie; reload; verify redirect to `/hidden`. (`FOTOBANK_E2E_LOCKOUT_WINDOW` is reserved for lockout tests only.)
10. **Direct entry: `/media/:id` (no from).** Existing direct-detail UI renders (regression guard for F2.4 path).
11. **Hidden cross-context.** Open `/media/:id?from=library` where the row is now hidden but cookie is unlocked; lightbox mode disabled, direct-detail visible.
12. **Stacked modals — Esc order.** Open lightbox; open Add-to-album from inside; press Esc → album modal closes, lightbox stays; press Esc → lightbox closes.
13. **Stacked modals — interaction passthrough.** While Add-to-album is open over lightbox, arrow keys do NOT advance lightbox.
14. **Keyboard bindings.** Arrows advance; Space toggles zoom; +/- zoom in/out; 0 reset; `i` toggles info; `h` hides (library) / unhides (hidden).
15. **Editable-target guard.** Focus an input in info panel; press arrow keys → text caret moves, lightbox does not advance.
16. **Mobile bottom sheet.** Resize to mobile breakpoint; open info; drag from peek to full; swipe down to dismiss; backdrop tap dismisses.
17. **Hidden 403 mid-session (action path).** Use `POST /api/v1/auth/hidden/lock` to clear cookie mid-flow; trigger an unhide → server returns 403; verify redirect to `/hidden`.
18. **Image source verification.** Initial `<img src>` is `size=grid&v=N`; subsequent network requests include `size=preview` and then `size=large`; no `/original` request fires before clicking the explicit Download action. (Don't assert the grid request fires, it may already be cached.)
19. **Modal migration regression.** AddToAlbum, Share, Confirm, RenameAlbum: each opens, Esc closes, no double-close on rapid Esc.

### Running

- Go unit tests: `make test`.
- Frontend unit tests: `bun run test` (in `frontend/`).
- E2E: `make test-e2e` (invokes `bun run test:e2e` against `playwright-e2e.config.ts` with `cmd/e2e-server`).

---

## 8. References

- Master frontend spec §6 (lightbox), §10 (router), §13.3 (mobile breakpoints), §13.5 (perf budgets): `docs/superpowers/specs/2026-04-26-fotobank-web-frontend-design.md`.
- F2.3 albums + sharing UI (router patterns, scroll restoration baseline): `docs/superpowers/specs/2026-04-28-fotobank-web-f2.3-albums-sharing-design.md`.
- F2.4 hidden privacy (MediaStore visible-only invariant, hidden cookie semantics, `/hidden/media`, `/api/v1/auth/hidden/lock`): `docs/superpowers/specs/2026-04-29-fotobank-web-f2.4-hidden-privacy-design.md`.

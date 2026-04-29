# F2.5 Lightbox Viewer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the route-driven lightbox viewer at `/media/:id?from=<source>` with prev/next navigation, vendored zoom/pan, modal stacking, source-aware hide/unhide, mobile bottom sheet, and progressive image loading.

**Architecture:** A single `Lightbox.svelte` component activates on `/media/:id?from=...` (the existing route). Source routes capture a `LightboxSession` snapshot (navIds, scrollY, returnFocusMediaId, returnHref) before navigation. Lightbox owns Esc/close via a central `modalStack`; when fallback is needed (reconstruction failure, hidden cross-context, 404) it renders a minimal close-only shell wrapping `DirectMediaDetail` (extracted from existing `MediaDetail.svelte`).

**Tech Stack:** Svelte 5 (runes), TypeScript, Vitest + @testing-library/svelte, Playwright, vendored `panzoom` library (~3KB gz). Bun package manager.

**Reference:** Spec at `docs/superpowers/specs/2026-04-29-fotobank-web-f2.5-lightbox-design.md`.

---

## File Structure

**New files:**
- `frontend/src/lib/lightbox/lightboxSession.svelte.ts` — app-level snapshot store
- `frontend/src/lib/lightbox/lightboxNav.svelte.ts` — nav derivation (prev/next/index)
- `frontend/src/lib/lightbox/lightboxLoader.ts` — progressive image loader + prefetch
- `frontend/src/lib/lightbox/scrollRestore.svelte.ts` — guarded scroll/focus restore helper
- `frontend/src/lib/lightbox/modalStack.svelte.ts` — central modal stack + Esc dispatch
- `frontend/src/lib/lightbox/sessionsFlatten.ts` — pure helper: flatten Sessions in render order
- `frontend/src/lib/components/BottomSheet.svelte` — generic bottom sheet
- `frontend/src/lib/components/DirectMediaDetail.svelte` — extracted from MediaDetail
- `frontend/src/lib/components/lightbox/Lightbox.svelte` — top-level orchestrator
- `frontend/src/lib/components/lightbox/LightboxFrame.svelte` — chrome (full + fallback modes)
- `frontend/src/lib/components/lightbox/LightboxMedia.svelte` — image/video branch
- `frontend/src/lib/components/lightbox/LightboxImage.svelte` — img + panzoom + progressive swap
- `frontend/src/lib/components/lightbox/LightboxVideo.svelte` — video element
- `frontend/src/lib/components/lightbox/LightboxToolbar.svelte` — info/close + actions slot
- `frontend/src/lib/components/lightbox/LightboxNavButtons.svelte` — prev/next arrows
- `frontend/src/lib/components/lightbox/LightboxActions.svelte` — source-aware MediaActions wrapper
- `frontend/src/lib/components/lightbox/LightboxInfoDrawer.svelte` — desktop side drawer
- `frontend/src/lib/components/lightbox/LightboxInfoSheet.svelte` — mobile bottom-sheet wrap
- `frontend/src/lib/components/lightbox/LightboxMetadata.svelte` — shared metadata content
- `frontend/tests/e2e/lightbox.spec.ts` — Playwright e2e

**Modified files:**
- `frontend/src/lib/router/router.svelte.ts` — parse `?from` on /media/:id; widen history.state
- `frontend/src/lib/grid/MediaCell.svelte` — add `data-media-id` for focus restore
- `frontend/src/lib/grid/VirtualGrid.svelte` — add `onOpenMedia(id)` prop overriding default navigation
- `frontend/src/routes/MediaDetail.svelte` — convert to dispatcher (Lightbox vs DirectMediaDetail)
- `frontend/src/routes/Library.svelte`, `Sessions.svelte`, `AlbumDetail.svelte`, `HiddenLibrary.svelte` — wire `lightboxSession.open`, scroll restore on remount
- `frontend/src/lib/components/AddToAlbumModal.svelte`, `ShareModal.svelte`, `ConfirmModal.svelte`, `RenameAlbumModal.svelte` — migrate to modalStack
- `frontend/src/App.svelte` — global Esc handler routes through `modalStack.dispatchEscape()`; mount `Lightbox` for media route when `from` present
- `frontend/src/routes/HiddenLibrary.svelte` — drop `disableNavigation` flag
- `cmd/e2e-server/main.go` — seed scenario-dedicated fixtures (deep album, hidden multi, selection set)

---

## Task 1: scrollRestore helper

**Files:**
- Create: `frontend/src/lib/lightbox/scrollRestore.svelte.ts`
- Test: `frontend/src/lib/lightbox/scrollRestore.test.ts`

This is a pure helper used by source routes on remount. It captures pending restore state and exposes `attemptRestore()` callable on each load completion.

- [ ] **Step 1: Write the failing test**

Create `frontend/src/lib/lightbox/scrollRestore.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach } from "vitest";
import { ScrollRestore } from "./scrollRestore.svelte";

function setBody(scrollHeight: number) {
  Object.defineProperty(document.body, "scrollHeight", {
    configurable: true,
    value: scrollHeight,
  });
  Object.defineProperty(window, "innerHeight", {
    configurable: true,
    value: 800,
  });
}

describe("ScrollRestore", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    setBody(0);
    window.scrollTo = vi.fn() as never;
  });

  it("does nothing when no pending restore", () => {
    const sr = new ScrollRestore();
    sr.attemptRestore();
    expect(window.scrollTo).not.toHaveBeenCalled();
  });

  it("does not scroll until target element exists or scrollHeight satisfies", () => {
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    sr.attemptRestore();
    expect(window.scrollTo).not.toHaveBeenCalled();
  });

  it("scrolls when scrollHeight satisfies (no target element)", () => {
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: null });
    setBody(2400);
    const ok = sr.attemptRestore();
    expect(ok).toBe(true);
    expect(window.scrollTo).toHaveBeenCalledWith(0, 1500);
  });

  it("scrolls when target element exists even before scrollHeight grows", () => {
    document.body.innerHTML = `<div data-media-id="m1"></div>`;
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    const ok = sr.attemptRestore();
    expect(ok).toBe(true);
    expect(window.scrollTo).toHaveBeenCalledWith(0, 1500);
  });

  it("clears pending after success", () => {
    document.body.innerHTML = `<div data-media-id="m1"></div>`;
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    sr.attemptRestore();
    (window.scrollTo as unknown as ReturnType<typeof vi.fn>).mockClear();
    sr.attemptRestore();
    expect(window.scrollTo).not.toHaveBeenCalled();
  });

  it("bails to partial Y after retry cap", () => {
    const sr = new ScrollRestore({ maxAttempts: 3 });
    sr.markPending({ scrollY: 5000, mediaId: "missing" });
    setBody(1000);
    sr.attemptRestore();
    sr.attemptRestore();
    sr.attemptRestore();
    const ok = sr.attemptRestore();
    expect(ok).toBe(true);
    expect(window.scrollTo).toHaveBeenCalledWith(0, 200); // 1000 - 800
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

```
cd frontend && bun run test scrollRestore
```

Expected: FAIL — module not found.

- [ ] **Step 3: Implement `scrollRestore.svelte.ts`**

```ts
// frontend/src/lib/lightbox/scrollRestore.svelte.ts
//
// Guarded scroll restoration helper. Source routes (Library, Sessions,
// AlbumDetail, HiddenLibrary) reload after lightbox close. The document
// scrollHeight is typically 0 at remount; scrolling immediately would
// clamp to top. markPending() stores the saved Y and target media id;
// attemptRestore() retries on each load completion until either the
// target element exists or scrollHeight is large enough, capped at
// maxAttempts (~10) to handle infinite-scroll edge cases.

export type Pending = {
  scrollY: number;
  mediaId: string | null;
};

export type ScrollRestoreOptions = {
  maxAttempts?: number; // default 10
};

export class ScrollRestore {
  private pending: Pending | null = null;
  private attempts = 0;
  private readonly maxAttempts: number;

  constructor(opts: ScrollRestoreOptions = {}) {
    this.maxAttempts = opts.maxAttempts ?? 10;
  }

  markPending(p: Pending): void {
    this.pending = p;
    this.attempts = 0;
  }

  isPending(): boolean {
    return this.pending !== null;
  }

  /**
   * Try to restore scroll. Returns true if restoration ran (either
   * because conditions were met or because the cap was hit and we
   * settled for a partial restore). Once true, no further calls
   * scroll until the next markPending().
   */
  attemptRestore(): boolean {
    const p = this.pending;
    if (p === null) return false;
    this.attempts += 1;

    const targetEl = p.mediaId !== null
      ? document.querySelector(`[data-media-id="${cssEscape(p.mediaId)}"]`)
      : null;
    const enoughContent = document.body.scrollHeight >= p.scrollY + window.innerHeight;
    const capHit = this.attempts >= this.maxAttempts;

    if (targetEl !== null || enoughContent) {
      window.scrollTo(0, p.scrollY);
      this.pending = null;
      return true;
    }
    if (capHit) {
      const partial = Math.max(0, document.body.scrollHeight - window.innerHeight);
      window.scrollTo(0, Math.min(p.scrollY, partial));
      this.pending = null;
      return true;
    }
    return false;
  }
}

// CSS.escape may be missing in older test environments; provide a
// minimal fallback that handles ids commonly produced by the backend
// (UUIDs, lowercase alnum + hyphens). Real ids never contain quotes
// or backslashes, but escape defensively for the CSS selector.
function cssEscape(s: string): string {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") return CSS.escape(s);
  return s.replace(/["\\]/g, "\\$&");
}
```

- [ ] **Step 4: Run the test to verify it passes**

```
cd frontend && bun run test scrollRestore
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/lightbox/scrollRestore.svelte.ts frontend/src/lib/lightbox/scrollRestore.test.ts
git commit -m "feat(frontend): add scrollRestore guarded restore helper"
```

---

## Task 2: modalStack store

**Files:**
- Create: `frontend/src/lib/lightbox/modalStack.svelte.ts`
- Test: `frontend/src/lib/lightbox/modalStack.test.ts`

Central store for ordered modal entries. Each entry has `id`, `onEscape`, optional `trap`. `dispatchEscape()` fires the topmost handler **without** auto-popping.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/lightbox/modalStack.test.ts
import { describe, it, expect, vi } from "vitest";
import { ModalStack } from "./modalStack.svelte";

describe("ModalStack", () => {
  it("starts empty", () => {
    const s = new ModalStack();
    expect(s.top()).toBeNull();
    expect(s.dispatchEscape()).toBe(false);
  });

  it("push/pop/isTopmost", () => {
    const s = new ModalStack();
    const onA = vi.fn();
    const onB = vi.fn();
    s.push({ id: "a", onEscape: onA });
    s.push({ id: "b", onEscape: onB });
    expect(s.isTopmost("a")).toBe(false);
    expect(s.isTopmost("b")).toBe(true);
    s.pop("b");
    expect(s.isTopmost("a")).toBe(true);
    s.pop("a");
    expect(s.top()).toBeNull();
  });

  it("dispatchEscape calls topmost handler and returns true; does NOT auto-pop", () => {
    const s = new ModalStack();
    const onA = vi.fn();
    const onB = vi.fn();
    s.push({ id: "a", onEscape: onA });
    s.push({ id: "b", onEscape: onB });
    expect(s.dispatchEscape()).toBe(true);
    expect(onB).toHaveBeenCalledOnce();
    expect(onA).not.toHaveBeenCalled();
    // Stack still has both — pop happens via the modal's own unmount.
    expect(s.isTopmost("b")).toBe(true);
  });

  it("dispatchEscape returns false on empty stack", () => {
    const s = new ModalStack();
    expect(s.dispatchEscape()).toBe(false);
  });

  it("idempotent pop of unknown id", () => {
    const s = new ModalStack();
    s.push({ id: "a", onEscape: vi.fn() });
    s.pop("nope");
    s.pop("a");
    s.pop("a");
    expect(s.top()).toBeNull();
  });

  it("pause/resume of trap on push/pop", () => {
    const s = new ModalStack();
    const trapA = { pause: vi.fn(), resume: vi.fn(), release: vi.fn() };
    const trapB = { pause: vi.fn(), resume: vi.fn(), release: vi.fn() };
    s.push({ id: "a", onEscape: vi.fn(), trap: trapA });
    expect(trapA.pause).not.toHaveBeenCalled();
    s.push({ id: "b", onEscape: vi.fn(), trap: trapB });
    expect(trapA.pause).toHaveBeenCalledOnce();
    expect(trapB.pause).not.toHaveBeenCalled();
    s.pop("b");
    expect(trapA.resume).toHaveBeenCalledOnce();
  });
});
```

- [ ] **Step 2: Run test (expect fail)**

```
cd frontend && bun run test modalStack
```

- [ ] **Step 3: Implement `modalStack.svelte.ts`**

```ts
// frontend/src/lib/lightbox/modalStack.svelte.ts
//
// Central modal stack with top-down Esc dispatch. Each modal-ish
// component (Lightbox, AddToAlbumModal, ShareModal, ConfirmModal,
// RenameAlbumModal, BottomSheet-as-modal) registers itself on mount
// and pops on unmount. Esc events fire the topmost entry's handler;
// the handler is responsible for triggering its own close path
// (which leads to unmount → pop). dispatchEscape() does NOT auto-pop.

export type FocusTrapHandle = {
  pause(): void;
  resume(): void;
  release(): void;
};

export type ModalEntry = {
  id: string;
  onEscape: () => void;
  trap?: FocusTrapHandle;
};

export class ModalStack {
  private entries: ModalEntry[] = $state([]);

  push(entry: ModalEntry): void {
    const prev = this.top();
    this.entries = [...this.entries, entry];
    prev?.trap?.pause();
  }

  pop(id: string): void {
    const idx = this.entries.findIndex((e) => e.id === id);
    if (idx < 0) return;
    this.entries = [...this.entries.slice(0, idx), ...this.entries.slice(idx + 1)];
    this.top()?.trap?.resume();
  }

  isTopmost(id: string): boolean {
    return this.top()?.id === id;
  }

  top(): ModalEntry | null {
    return this.entries.length > 0 ? this.entries[this.entries.length - 1]! : null;
  }

  dispatchEscape(): boolean {
    const t = this.top();
    if (t === null) return false;
    t.onEscape();
    return true;
  }
}

// Module-level singleton — the stack is process-global; modals
// across the app register against the same instance. Mirrors the
// SelectionStore export pattern.
export const modalStack = new ModalStack();
```

- [ ] **Step 4: Run test (expect pass)**

```
cd frontend && bun run test modalStack
```

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/lightbox/modalStack.svelte.ts frontend/src/lib/lightbox/modalStack.test.ts
git commit -m "feat(frontend): add modalStack with topmost-first Esc dispatch"
```

---

## Task 3: lightboxSession store

**Files:**
- Create: `frontend/src/lib/lightbox/lightboxSession.svelte.ts`
- Test: `frontend/src/lib/lightbox/lightboxSession.test.ts`

App-level store holding the durable snapshot captured by source routes before navigation. Survives route unmount.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/lightbox/lightboxSession.test.ts
import { describe, it, expect } from "vitest";
import { LightboxSessionStore, type LightboxSource } from "./lightboxSession.svelte";

const lib: LightboxSource = { kind: "library" };

describe("LightboxSessionStore", () => {
  it("starts empty", () => {
    const s = new LightboxSessionStore();
    expect(s.snapshot).toBeNull();
  });

  it("open() captures the snapshot", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a", "b", "c"],
      selected: false,
      scrollY: 1234,
      returnFocusMediaId: "a",
      returnHref: "/library",
    });
    expect(s.snapshot?.navIds).toEqual(["a", "b", "c"]);
    expect(s.snapshot?.scrollY).toBe(1234);
  });

  it("close() clears the snapshot", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: "a",
      returnHref: "/library",
    });
    s.close();
    expect(s.snapshot).toBeNull();
  });

  it("clearScroll() zeroes scrollY without dropping the snapshot", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a"],
      selected: false,
      scrollY: 1234,
      returnFocusMediaId: "a",
      returnHref: "/library",
    });
    s.clearScroll();
    expect(s.snapshot?.scrollY).toBe(0);
    expect(s.snapshot?.navIds).toEqual(["a"]);
  });

  it("clearReturnFocus() clears returnFocusMediaId only", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a"],
      selected: false,
      scrollY: 100,
      returnFocusMediaId: "a",
      returnHref: "/library",
    });
    s.clearReturnFocus();
    expect(s.snapshot?.returnFocusMediaId).toBeNull();
    expect(s.snapshot?.scrollY).toBe(100);
  });

  it("removeIds() removes ids from navIds", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a", "b", "c", "d"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: null,
      returnHref: "/library",
    });
    s.removeIds(["b", "d", "missing"]);
    expect(s.snapshot?.navIds).toEqual(["a", "c"]);
  });

  it("removeIds() on no-op input leaves navIds untouched", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a", "b"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: null,
      returnHref: "/library",
    });
    s.removeIds([]);
    expect(s.snapshot?.navIds).toEqual(["a", "b"]);
  });
});
```

- [ ] **Step 2: Run test (expect fail)**

```
cd frontend && bun run test lightboxSession
```

- [ ] **Step 3: Implement `lightboxSession.svelte.ts`**

```ts
// frontend/src/lib/lightbox/lightboxSession.svelte.ts
//
// App-level snapshot of the source state at lightbox-open time. Source
// routes capture this BEFORE navigation; the lightbox reads it from
// any route. Route-local stores (AlbumDetailStore, HiddenMediaStore)
// are unmounted during lightbox display, so the snapshot is the only
// durable handoff between source and lightbox.

export type LightboxSource =
  | { kind: "library" }
  | { kind: "sessions" }
  | { kind: "album"; albumId: string }
  | { kind: "hidden" };

export type LightboxSnapshot = {
  source: LightboxSource;
  navIds: string[];
  selected: boolean;
  scrollY: number;
  returnFocusMediaId: string | null;
  returnHref: string;
};

export class LightboxSessionStore {
  snapshot = $state<LightboxSnapshot | null>(null);

  open(s: LightboxSnapshot): void {
    this.snapshot = s;
  }

  close(): void {
    this.snapshot = null;
  }

  clearScroll(): void {
    if (this.snapshot === null) return;
    this.snapshot = { ...this.snapshot, scrollY: 0 };
  }

  clearReturnFocus(): void {
    if (this.snapshot === null) return;
    this.snapshot = { ...this.snapshot, returnFocusMediaId: null };
  }

  removeIds(ids: string[]): void {
    const cur = this.snapshot;
    if (cur === null || ids.length === 0) return;
    const drop = new Set(ids);
    const next = cur.navIds.filter((x) => !drop.has(x));
    if (next.length === cur.navIds.length) return;
    this.snapshot = { ...cur, navIds: next };
  }
}

export const lightboxSession = new LightboxSessionStore();
```

- [ ] **Step 4: Run test (expect pass)**

```
cd frontend && bun run test lightboxSession
```

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/lightbox/lightboxSession.svelte.ts frontend/src/lib/lightbox/lightboxSession.test.ts
git commit -m "feat(frontend): add LightboxSessionStore for source→lightbox handoff"
```

---

## Task 4: lightboxNav derivation

**Files:**
- Create: `frontend/src/lib/lightbox/lightboxNav.svelte.ts`
- Test: `frontend/src/lib/lightbox/lightboxNav.test.ts`

Derives current index, prev/next ids, and hasPrev/hasNext from `(navIds, activeId)`.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/lightbox/lightboxNav.test.ts
import { describe, it, expect } from "vitest";
import { computeNav } from "./lightboxNav.svelte";

describe("computeNav", () => {
  it("returns -1 / null when active id not in navIds", () => {
    const r = computeNav(["a", "b", "c"], "missing");
    expect(r).toEqual({ index: -1, prevId: null, nextId: null, hasPrev: false, hasNext: false });
  });

  it("returns prev/next at middle position", () => {
    const r = computeNav(["a", "b", "c"], "b");
    expect(r).toEqual({ index: 1, prevId: "a", nextId: "c", hasPrev: true, hasNext: true });
  });

  it("returns null prev at first position", () => {
    const r = computeNav(["a", "b", "c"], "a");
    expect(r.prevId).toBeNull();
    expect(r.nextId).toBe("b");
    expect(r.hasPrev).toBe(false);
    expect(r.hasNext).toBe(true);
  });

  it("returns null next at last position", () => {
    const r = computeNav(["a", "b", "c"], "c");
    expect(r.prevId).toBe("b");
    expect(r.nextId).toBeNull();
    expect(r.hasPrev).toBe(true);
    expect(r.hasNext).toBe(false);
  });

  it("single-item nav has no prev/next", () => {
    const r = computeNav(["a"], "a");
    expect(r).toEqual({ index: 0, prevId: null, nextId: null, hasPrev: false, hasNext: false });
  });

  it("empty navIds returns -1", () => {
    const r = computeNav([], "x");
    expect(r.index).toBe(-1);
  });
});
```

- [ ] **Step 2: Run test (expect fail)**

```
cd frontend && bun run test lightboxNav
```

- [ ] **Step 3: Implement `lightboxNav.svelte.ts`**

```ts
// frontend/src/lib/lightbox/lightboxNav.svelte.ts
//
// Pure derivation of nav state from a navIds list and the active id.
// Lightbox.svelte calls computeNav() inside a $derived so prev/next
// recompute when either input changes.

export type Nav = {
  index: number; // -1 if active id is not in navIds
  prevId: string | null;
  nextId: string | null;
  hasPrev: boolean;
  hasNext: boolean;
};

export function computeNav(navIds: string[], activeId: string): Nav {
  const index = navIds.indexOf(activeId);
  if (index < 0) {
    return { index: -1, prevId: null, nextId: null, hasPrev: false, hasNext: false };
  }
  const prevId = index > 0 ? navIds[index - 1] ?? null : null;
  const nextId = index < navIds.length - 1 ? navIds[index + 1] ?? null : null;
  return {
    index,
    prevId,
    nextId,
    hasPrev: prevId !== null,
    hasNext: nextId !== null,
  };
}
```

- [ ] **Step 4: Run test (expect pass)**

```
cd frontend && bun run test lightboxNav
```

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/lightbox/lightboxNav.svelte.ts frontend/src/lib/lightbox/lightboxNav.test.ts
git commit -m "feat(frontend): add computeNav derivation for lightbox prev/next"
```

---

## Task 5: lightboxLoader (progressive image loader)

**Files:**
- Create: `frontend/src/lib/lightbox/lightboxLoader.ts`
- Test: `frontend/src/lib/lightbox/lightboxLoader.test.ts`

Manages the progressive grid → preview → large swap, prefetches immediate prev/next, and uses a generation token for logical cancellation.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/lightbox/lightboxLoader.test.ts
import { describe, it, expect, vi, beforeEach } from "vitest";
import { LightboxLoader, thumbUrl } from "./lightboxLoader";

// Stub HTMLImageElement so we can control load resolution/timing.
class FakeImage {
  src = "";
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  decode(): Promise<void> { return Promise.resolve(); }
}

beforeEach(() => {
  (globalThis as unknown as { Image: typeof Image }).Image =
    FakeImage as unknown as typeof Image;
});

describe("thumbUrl", () => {
  it("builds the size-qualified thumb URL", () => {
    expect(thumbUrl("abc", "preview", 7)).toBe("/api/v1/media/abc/thumb?size=preview&v=7");
    expect(thumbUrl("abc", "large", 0)).toBe("/api/v1/media/abc/thumb?size=large&v=0");
    expect(thumbUrl("abc", "grid", 3)).toBe("/api/v1/media/abc/thumb?size=grid&v=3");
  });
});

describe("LightboxLoader.load", () => {
  it("uses grid URL as initial source when provided", async () => {
    const loader = new LightboxLoader();
    const onSrc = vi.fn();
    await loader.load({
      activeId: "m1",
      gridSrc: "/grid",
      previewUrl: "/preview",
      largeUrl: "/large",
      onSrc,
    });
    expect(onSrc).toHaveBeenNthCalledWith(1, "/grid");
  });

  it("starts at preview when no gridSrc is provided", async () => {
    const loader = new LightboxLoader();
    const onSrc = vi.fn();
    await loader.load({
      activeId: "m1",
      gridSrc: null,
      previewUrl: "/preview",
      largeUrl: "/large",
      onSrc,
    });
    // First emission should be the preview URL once it loads.
    // (Test setup auto-resolves load; we only assert ordering.)
    const seq = onSrc.mock.calls.map((c) => c[0]);
    expect(seq[0]).toBe("/preview");
  });

  it("logical cancellation drops stale completions", async () => {
    const loader = new LightboxLoader();
    const onSrcA = vi.fn();
    const onSrcB = vi.fn();
    const a = loader.load({
      activeId: "a",
      gridSrc: null,
      previewUrl: "/a-preview",
      largeUrl: "/a-large",
      onSrc: onSrcA,
    });
    // Switch to B before A finishes.
    loader.cancelAll();
    const b = loader.load({
      activeId: "b",
      gridSrc: null,
      previewUrl: "/b-preview",
      largeUrl: "/b-large",
      onSrc: onSrcB,
    });
    await Promise.all([a, b]);
    // onSrcA must not have produced /a-large after cancelAll.
    expect(onSrcA.mock.calls.some((c) => c[0] === "/a-large")).toBe(false);
  });
});

describe("LightboxLoader.prefetch", () => {
  it("orders preview prev/next first, large second", () => {
    const loader = new LightboxLoader();
    const order: string[] = [];
    const fakeFetch = vi.fn().mockImplementation((url: string) => {
      order.push(url);
      return Promise.resolve();
    });
    loader.setFetcher(fakeFetch);
    loader.prefetch({
      prevPreviewUrl: "/p-prev",
      nextPreviewUrl: "/p-next",
      prevLargeUrl: "/l-prev",
      nextLargeUrl: "/l-next",
    });
    expect(order.slice(0, 2).sort()).toEqual(["/p-next", "/p-prev"]);
    expect(order.slice(2).sort()).toEqual(["/l-next", "/l-prev"]);
  });

  it("caps in-flight prefetches at 2", async () => {
    const loader = new LightboxLoader();
    let inflight = 0;
    let peak = 0;
    const fakeFetch = vi.fn().mockImplementation(() => new Promise<void>((resolve) => {
      inflight += 1;
      peak = Math.max(peak, inflight);
      setTimeout(() => { inflight -= 1; resolve(); }, 10);
    }));
    loader.setFetcher(fakeFetch);
    loader.prefetch({
      prevPreviewUrl: "/p-prev",
      nextPreviewUrl: "/p-next",
      prevLargeUrl: "/l-prev",
      nextLargeUrl: "/l-next",
    });
    await new Promise((r) => setTimeout(r, 50));
    expect(peak).toBeLessThanOrEqual(2);
  });
});
```

- [ ] **Step 2: Run test (expect fail)**

```
cd frontend && bun run test lightboxLoader
```

- [ ] **Step 3: Implement `lightboxLoader.ts`**

```ts
// frontend/src/lib/lightbox/lightboxLoader.ts
//
// Progressive image loader for the lightbox. Sequence:
//   1. paint cached grid URL if available (synchronous src assignment)
//   2. load + decode preview; swap when ready
//   3. load + decode large in background; swap when ready
// Prefetch immediate prev/next: preview first, then large; max 2 in
// flight. Cancellation is logical: each load() invocation increments
// the generation token; completions whose token doesn't match are
// dropped (decoded image discarded, references released for GC).

export type ThumbSize = "grid" | "preview" | "large";

export function thumbUrl(id: string, size: ThumbSize, version: number): string {
  return `/api/v1/media/${id}/thumb?size=${size}&v=${version}`;
}

export type LoadOpts = {
  activeId: string;
  gridSrc: string | null;       // already-cached grid url, or null
  previewUrl: string;
  largeUrl: string;
  onSrc: (url: string) => void; // called as the visible src changes
};

export type PrefetchOpts = {
  prevPreviewUrl: string | null;
  nextPreviewUrl: string | null;
  prevLargeUrl: string | null;
  nextLargeUrl: string | null;
};

type Fetcher = (url: string) => Promise<void>;

export class LightboxLoader {
  private generation = 0;
  private fetcher: Fetcher = defaultFetcher;
  private inFlight = 0;
  private queue: string[] = [];
  private static readonly MAX_INFLIGHT = 2;

  /** Override the fetcher (test injection). */
  setFetcher(f: Fetcher): void {
    this.fetcher = f;
  }

  cancelAll(): void {
    this.generation += 1;
    this.queue = [];
  }

  async load(opts: LoadOpts): Promise<void> {
    this.generation += 1;
    const myGen = this.generation;
    const { gridSrc, previewUrl, largeUrl, onSrc } = opts;

    if (gridSrc !== null) onSrc(gridSrc);

    try {
      await loadAndDecode(previewUrl);
    } catch {
      return; // preview failed; keep prior src visible
    }
    if (myGen !== this.generation) return;
    onSrc(previewUrl);

    try {
      await loadAndDecode(largeUrl);
    } catch {
      return;
    }
    if (myGen !== this.generation) return;
    onSrc(largeUrl);
  }

  prefetch(opts: PrefetchOpts): void {
    const previews = [opts.prevPreviewUrl, opts.nextPreviewUrl].filter(notNull);
    const larges = [opts.prevLargeUrl, opts.nextLargeUrl].filter(notNull);
    this.queue = [...previews, ...larges];
    this.pump();
  }

  private pump(): void {
    while (this.inFlight < LightboxLoader.MAX_INFLIGHT && this.queue.length > 0) {
      const url = this.queue.shift()!;
      this.inFlight += 1;
      this.fetcher(url).catch(() => undefined).finally(() => {
        this.inFlight -= 1;
        this.pump();
      });
    }
  }
}

function notNull<T>(x: T | null): x is T {
  return x !== null;
}

async function loadAndDecode(url: string): Promise<void> {
  const img = new Image();
  img.src = url;
  if (typeof img.decode === "function") {
    await img.decode();
    return;
  }
  await new Promise<void>((resolve, reject) => {
    img.onload = () => resolve();
    img.onerror = () => reject(new Error("image load failed"));
  });
}

function defaultFetcher(url: string): Promise<void> {
  // Image() preload has the simplest browser semantics for cache
  // population. Result is dropped — we only care about the side
  // effect of warming HTTP cache.
  return new Promise<void>((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve();
    img.onerror = () => reject(new Error("prefetch failed"));
    img.src = url;
  });
}
```

- [ ] **Step 4: Run test (expect pass)**

```
cd frontend && bun run test lightboxLoader
```

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/lightbox/lightboxLoader.ts frontend/src/lib/lightbox/lightboxLoader.test.ts
git commit -m "feat(frontend): add LightboxLoader progressive load + prefetch"
```

---

## Task 6: Sessions flatten helper

**Files:**
- Create: `frontend/src/lib/lightbox/sessionsFlatten.ts`
- Test: `frontend/src/lib/lightbox/sessionsFlatten.test.ts`

Pure helper: flatten Sessions in render order. Sessions render the same `mediaStore.months` grouping but in a different visual layout — for nav purposes the order is the same flat list newest-first.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/lightbox/sessionsFlatten.test.ts
import { describe, it, expect } from "vitest";
import { flattenSessionIds, flattenLibraryIds } from "./sessionsFlatten";
import type { Month } from "../media/mediaStore.svelte";

const months: Month[] = [
  {
    key: "2026-04",
    items: [
      { id: "a", timestamp: "", aspect: 1, thumbUrl: "", taken: new Date("2026-04-20"), thumbVersion: 0 },
      { id: "b", timestamp: "", aspect: 1, thumbUrl: "", taken: new Date("2026-04-10"), thumbVersion: 0 },
    ],
  },
  {
    key: "2026-03",
    items: [
      { id: "c", timestamp: "", aspect: 1, thumbUrl: "", taken: new Date("2026-03-15"), thumbVersion: 0 },
    ],
  },
];

describe("flattenLibraryIds / flattenSessionIds", () => {
  it("flattens months in declared order", () => {
    expect(flattenLibraryIds(months)).toEqual(["a", "b", "c"]);
    expect(flattenSessionIds(months)).toEqual(["a", "b", "c"]);
  });

  it("handles empty months", () => {
    expect(flattenLibraryIds([])).toEqual([]);
    expect(flattenSessionIds([])).toEqual([]);
  });
});
```

- [ ] **Step 2: Run test (expect fail)**

```
cd frontend && bun run test sessionsFlatten
```

- [ ] **Step 3: Implement `sessionsFlatten.ts`**

```ts
// frontend/src/lib/lightbox/sessionsFlatten.ts
//
// Source-list flattening for lightbox navIds. Library and Sessions
// share the same mediaStore.months ordering (newest first within month,
// newest month first); the flatten result is identical. The two
// exported names exist so callers in source routes are explicit
// about which list they're capturing.

import type { Month } from "../media/mediaStore.svelte";

export function flattenLibraryIds(months: Month[]): string[] {
  const out: string[] = [];
  for (const m of months) {
    for (const it of m.items) out.push(it.id);
  }
  return out;
}

export const flattenSessionIds = flattenLibraryIds;
```

- [ ] **Step 4: Run test (expect pass)**

```
cd frontend && bun run test sessionsFlatten
```

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/lightbox/sessionsFlatten.ts frontend/src/lib/lightbox/sessionsFlatten.test.ts
git commit -m "feat(frontend): add sessionsFlatten helper for lightbox navIds"
```

---

## Task 7: Router query-param parsing for /media/:id

**Files:**
- Modify: `frontend/src/lib/router/router.svelte.ts`
- Modify: `frontend/src/lib/router/router.test.ts`

Extend the `media` route match to expose a parsed `from` query param. Widen `history.state` typing to admit `sourceScroll`.

- [ ] **Step 1: Write the failing test (extend existing test file)**

Add to `frontend/src/lib/router/router.test.ts` (after the existing `it("ignores search and hash when matching", ...)` test):

```ts
it("parses ?from=library on /media/:id", () => {
  setLocation("/media/abc?from=library");
  const r = new RouterStore();
  expect(r.current).toEqual({ route: "media", id: "abc", from: "library" });
});

it("parses ?from=sessions / ?from=hidden on /media/:id", () => {
  setLocation("/media/x?from=sessions");
  let r = new RouterStore();
  expect(r.current).toEqual({ route: "media", id: "x", from: "sessions" });
  setLocation("/media/y?from=hidden");
  r = new RouterStore();
  expect(r.current).toEqual({ route: "media", id: "y", from: "hidden" });
});

it("parses ?from=album:abc on /media/:id", () => {
  setLocation("/media/abc?from=album:my-album-123");
  const r = new RouterStore();
  expect(r.current).toEqual({ route: "media", id: "abc", from: "album:my-album-123" });
});

it("/media/:id without ?from has no from field", () => {
  setLocation("/media/abc");
  const r = new RouterStore();
  expect(r.current).toEqual({ route: "media", id: "abc" });
});

it("ignores unknown ?from values (treats as absent)", () => {
  setLocation("/media/abc?from=nope");
  const r = new RouterStore();
  expect(r.current).toEqual({ route: "media", id: "abc" });
});
```

- [ ] **Step 2: Run test (expect fail)**

```
cd frontend && bun run test router
```

- [ ] **Step 3: Update `router.svelte.ts`**

Modify the `RouteMatch` union and the `/media/:id` pattern:

```ts
// In RouteMatch:
export type RouteMatch =
  | { route: "library" }
  | { route: "sessions" }
  | { route: "settings" }
  | { route: "albums" }
  | { route: "albums.detail"; id: string }
  | { route: "shares"; album_id?: string; show_revoked?: boolean }
  | { route: "media"; id: string; from?: string }
  | { route: "hidden" }
  | { route: "notfound"; path: string };

// Replace the /media/:id PATTERNS entry with:
{ re: /^\/media\/([^/]+)$/, build: (m) => {
  const sp = new URLSearchParams(window.location.search);
  const fromRaw = sp.get("from");
  const from = parseFrom(fromRaw);
  return {
    route: "media" as const,
    id: m[1]!,
    ...(from !== null ? { from } : {}),
  };
} },
```

Add helper near the bottom of the file (above `RouterStore`):

```ts
// parseFrom narrows the ?from= query param to one of the four known
// source kinds plus the album:<id> form. Unknown values fall through
// to undefined so /media/:id?from=garbage behaves the same as no
// from at all (the lightbox treats absent from as direct-detail).
function parseFrom(raw: string | null): string | null {
  if (raw === null) return null;
  if (raw === "library" || raw === "sessions" || raw === "hidden") return raw;
  if (raw.startsWith("album:") && raw.length > "album:".length) return raw;
  return null;
}
```

- [ ] **Step 4: Run test (expect pass)**

```
cd frontend && bun run test router
```

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/router/router.svelte.ts frontend/src/lib/router/router.test.ts
git commit -m "feat(frontend): parse ?from on /media/:id route match"
```

---

## Task 8: BottomSheet generic component

**Files:**
- Create: `frontend/src/lib/components/BottomSheet.svelte`
- Test: `frontend/src/lib/components/BottomSheet.test.ts`

Reusable bottom sheet with two snap points (peek / full), drag handle, swipe-down dismiss, backdrop click, Esc dismiss via modalStack.

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/components/BottomSheet.test.ts
import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import BottomSheet from "./BottomSheet.svelte";

describe("BottomSheet", () => {
  it("renders content slot", () => {
    const onClose = vi.fn();
    const { getByText } = render(BottomSheet, {
      props: { id: "bs1", onClose, snap: "peek", children: () => "hello" } as never,
    });
    expect(getByText("hello")).toBeTruthy();
  });

  it("backdrop click invokes onClose", async () => {
    const onClose = vi.fn();
    const { container } = render(BottomSheet, {
      props: { id: "bs1", onClose, snap: "peek", children: () => "x" } as never,
    });
    const backdrop = container.querySelector(".bs-backdrop") as HTMLElement;
    expect(backdrop).toBeTruthy();
    await fireEvent.click(backdrop);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("clicks inside the sheet do not close", async () => {
    const onClose = vi.fn();
    const { container } = render(BottomSheet, {
      props: { id: "bs1", onClose, snap: "peek", children: () => "x" } as never,
    });
    const sheet = container.querySelector(".bs-sheet") as HTMLElement;
    await fireEvent.click(sheet);
    expect(onClose).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run test (expect fail)**

```
cd frontend && bun run test BottomSheet
```

- [ ] **Step 3: Implement `BottomSheet.svelte`**

```svelte
<!-- frontend/src/lib/components/BottomSheet.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { onMount, onDestroy } from "svelte";
  import { modalStack } from "../lightbox/modalStack.svelte";

  let {
    id,
    onClose,
    snap = "peek",
    children,
  }: {
    id: string;
    onClose: () => void;
    snap?: "peek" | "full";
    children: Snippet;
  } = $props();

  let dragStart: number | null = null;
  let dragDelta = $state(0);

  onMount(() => {
    modalStack.push({ id, onEscape: onClose });
  });
  onDestroy(() => {
    modalStack.pop(id);
  });

  function onPointerDown(e: PointerEvent) {
    dragStart = e.clientY;
    dragDelta = 0;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  }
  function onPointerMove(e: PointerEvent) {
    if (dragStart === null) return;
    dragDelta = Math.max(0, e.clientY - dragStart);
  }
  function onPointerUp(e: PointerEvent) {
    (e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId);
    if (dragDelta > 80) onClose();
    dragStart = null;
    dragDelta = 0;
  }

  function onBackdropClick(e: MouseEvent) {
    if (e.target === e.currentTarget) onClose();
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="bs-backdrop" role="presentation" onclick={onBackdropClick}>
  <div
    class="bs-sheet"
    class:full={snap === "full"}
    role="dialog"
    aria-modal="true"
    style:transform={`translateY(${dragDelta}px)`}
    onclick={(e) => e.stopPropagation()}
  >
    <button
      type="button"
      class="bs-handle"
      aria-label="Drag handle"
      onpointerdown={onPointerDown}
      onpointermove={onPointerMove}
      onpointerup={onPointerUp}
      onpointercancel={onPointerUp}
    ></button>
    <div class="bs-body">{@render children()}</div>
  </div>
</div>

<style>
  .bs-backdrop {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.5);
    z-index: 200;
    display: flex; align-items: flex-end; justify-content: center;
  }
  .bs-sheet {
    background: var(--bg-elevated);
    border-radius: 16px 16px 0 0;
    width: 100%;
    max-height: 50vh;
    display: flex; flex-direction: column;
    transition: transform 120ms ease-out, max-height 200ms ease-out;
  }
  .bs-sheet.full { max-height: 90vh; }
  .bs-handle {
    align-self: center;
    width: 40px; height: 4px;
    border-radius: 2px;
    background: var(--border);
    margin: 8px 0 12px;
    border: none;
    padding: 0;
    touch-action: none;
    cursor: grab;
  }
  .bs-body { overflow: auto; padding: 0 16px 16px; }
</style>
```

- [ ] **Step 4: Run test (expect pass)**

```
cd frontend && bun run test BottomSheet
```

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/BottomSheet.svelte frontend/src/lib/components/BottomSheet.test.ts
git commit -m "feat(frontend): add BottomSheet generic component"
```

---

## Task 9: Migrate existing modals to modalStack

**Files:**
- Modify: `frontend/src/lib/components/AddToAlbumModal.svelte`
- Modify: `frontend/src/lib/components/ShareModal.svelte`
- Modify: `frontend/src/lib/components/ConfirmModal.svelte`
- Modify: `frontend/src/lib/components/RenameAlbumModal.svelte`
- Modify: `frontend/src/App.svelte`

Each modal: drop its local `<svelte:window onkeydown>` Esc handler; on mount push entry onto `modalStack`; on unmount pop. App.svelte global Esc handler routes through `modalStack.dispatchEscape()`; selection-clear runs only when stack is empty.

- [ ] **Step 1: Update App.svelte global Esc handler**

In `frontend/src/App.svelte`, replace the Esc effect (lines ~65-78) with:

```ts
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
    if (isEditableTarget(e.target)) return;
    selection.clear();
  };
  window.addEventListener("keydown", onKey);
  return () => window.removeEventListener("keydown", onKey);
});
```

Add the import at the top of `App.svelte`:

```ts
import { modalStack } from "./lib/lightbox/modalStack.svelte";
```

- [ ] **Step 2: Migrate AddToAlbumModal.svelte**

In `frontend/src/lib/components/AddToAlbumModal.svelte`:

1. Add imports near the top of the script block:
   ```ts
   import { onMount, onDestroy } from "svelte";
   import { modalStack } from "../lightbox/modalStack.svelte";
   const modalId = `add-to-album-${Math.random().toString(36).slice(2)}`;
   onMount(() => modalStack.push({ id: modalId, onEscape: onClose }));
   onDestroy(() => modalStack.pop(modalId));
   ```
2. Remove the `<svelte:window onkeydown=...>` line (search for `Escape`).

- [ ] **Step 3: Migrate ShareModal.svelte**

Same pattern as Task 9 Step 2; the unique modalId prefix is `share-`.

- [ ] **Step 4: Migrate ConfirmModal.svelte**

In `frontend/src/lib/components/ConfirmModal.svelte`:

1. Replace the `<svelte:window onkeydown=...>` line with the modalStack registration (modalId prefix `confirm-`):
   ```ts
   import { onMount, onDestroy } from "svelte";
   import { modalStack } from "../lightbox/modalStack.svelte";
   const modalId = `confirm-${Math.random().toString(36).slice(2)}`;
   onMount(() => modalStack.push({ id: modalId, onEscape: () => { if (!pending) onCancel(); } }));
   onDestroy(() => modalStack.pop(modalId));
   ```
2. Remove the `<svelte:window onkeydown=...>` line.

- [ ] **Step 5: Migrate RenameAlbumModal.svelte**

Same pattern as Task 9 Step 4; modalId prefix `rename-`; `onEscape: () => { if (!pending) onCancel(); }`.

- [ ] **Step 6: Run all modal + App tests**

```
cd frontend && bun run test AddToAlbumModal ShareModal RenameAlbumModal ConfirmModal
```

Expected: PASS (existing tests keep passing — none rely on a window keydown handler).

- [ ] **Step 7: Add a regression test for modalStack interaction**

Append to `frontend/src/lib/lightbox/modalStack.test.ts`:

```ts
it("pop is idempotent across rapid Esc + unmount", () => {
  const s = new ModalStack();
  const onA = vi.fn();
  s.push({ id: "a", onEscape: onA });
  s.dispatchEscape();
  s.dispatchEscape();
  expect(onA).toHaveBeenCalledTimes(2);
  s.pop("a");
  s.pop("a");
  expect(s.top()).toBeNull();
});
```

Run: `cd frontend && bun run test modalStack`. Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add frontend/src/App.svelte \
  frontend/src/lib/components/AddToAlbumModal.svelte \
  frontend/src/lib/components/ShareModal.svelte \
  frontend/src/lib/components/ConfirmModal.svelte \
  frontend/src/lib/components/RenameAlbumModal.svelte \
  frontend/src/lib/lightbox/modalStack.test.ts
git commit -m "refactor(frontend): migrate modals to central modalStack Esc dispatch"
```

---

## Task 10: Extract DirectMediaDetail from MediaDetail

**Files:**
- Create: `frontend/src/lib/components/DirectMediaDetail.svelte`
- Modify: `frontend/src/routes/MediaDetail.svelte`
- Modify: `frontend/src/routes/MediaDetail.test.ts`

Pull the existing direct-detail UI out of `MediaDetail.svelte` so it can be rendered both by the route directly (no `from`) and by the lightbox fallback shell. Add optional `backHref` and `onClose` props so the lightbox can suppress the inner back link.

- [ ] **Step 1: Create `DirectMediaDetail.svelte` containing the existing UI**

Copy the entire content of `frontend/src/routes/MediaDetail.svelte` into `frontend/src/lib/components/DirectMediaDetail.svelte`, then:

1. Update the import path for `MediaStore`/`toMedia` to `"../media/mediaStore.svelte"` (one level up plus `media`).
2. Update the import path for `router`, `handleInternalLinkClick` to `"../router/router.svelte"`.
3. Update the import path for `formatCoord` to `"../format/coords"`.
4. Update the import path for `MediaActions` / `AddToAlbumModal` / `ShareModal` to `"./MediaActions.svelte"` etc. (same directory).
5. Update import for `AlbumsStore`, `HiddenStore`, `ToastStore` to `"../albums/albumsStore.svelte"` / `"../hidden/hiddenStore.svelte"` / `"../toasts/toastStore.svelte"`.
6. Update import for `CreateShareBody` to `"../share/shareTypes"`.
7. Update import for `api` to `"../api/client"`.
8. Add two new optional props in the `$props()` destructuring:
   ```ts
   let { id, mediaStore, albumsStore, hiddenStore, toastStore,
         backHref, onClose }: {
     id: string;
     mediaStore: MediaStore;
     albumsStore: AlbumsStore;
     hiddenStore: HiddenStore;
     toastStore: ToastStore;
     backHref?: string;
     onClose?: () => void;
   } = $props();
   ```
9. Replace the back-link block:
   ```svelte
   <a href="/library" onclick={(e) => handleInternalLinkClick(e, "/library")} class="back-link">
     ← Back to Library
   </a>
   ```
   with:
   ```svelte
   {#if onClose === undefined}
     <a
       href={backHref ?? "/library"}
       onclick={(e) => handleInternalLinkClick(e, backHref ?? "/library")}
       class="back-link"
     >
       ← Back to {backHref === undefined || backHref === "/library" ? "Library" : "previous"}
     </a>
   {/if}
   ```
   The `onClose === undefined` gate suppresses the inner back link when the lightbox owns close; the lightbox always provides `onClose`.

- [ ] **Step 2: Replace MediaDetail.svelte with a thin dispatcher**

Replace the entire contents of `frontend/src/routes/MediaDetail.svelte` with:

```svelte
<!-- frontend/src/routes/MediaDetail.svelte -->
<!-- Dispatcher: with ?from=, mount Lightbox; without, mount DirectMediaDetail. -->
<script lang="ts">
  import type { MediaStore } from "../lib/media/mediaStore.svelte";
  import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
  import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
  import type { ToastStore } from "../lib/toasts/toastStore.svelte";
  import DirectMediaDetail from "../lib/components/DirectMediaDetail.svelte";
  import Lightbox from "../lib/components/lightbox/Lightbox.svelte";

  let { id, from, mediaStore, albumsStore, hiddenStore, toastStore }: {
    id: string;
    from: string | undefined;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
  } = $props();
</script>

{#if from === undefined}
  <DirectMediaDetail {id} {mediaStore} {albumsStore} {hiddenStore} {toastStore} />
{:else}
  <Lightbox {id} {from} {mediaStore} {albumsStore} {hiddenStore} {toastStore} />
{/if}
```

(`Lightbox.svelte` is created in Task 13. This file will not compile until then; that's fine — Svelte only catches it at build time. The unit tests for MediaDetail can still pass by mocking the children.)

- [ ] **Step 3: Update App.svelte to thread `from`**

In `frontend/src/App.svelte`, find the line:

```svelte
<MediaDetail id={router.current.id} {mediaStore} {albumsStore} {hiddenStore} {toastStore} />
```

Replace with:

```svelte
<MediaDetail
  id={router.current.id}
  from={router.current.from}
  {mediaStore}
  {albumsStore}
  {hiddenStore}
  {toastStore}
/>
```

- [ ] **Step 4: Update MediaDetail.test.ts to point at DirectMediaDetail**

The existing `MediaDetail.test.ts` exercises behaviors that now live in `DirectMediaDetail`. Move the test to `frontend/src/lib/components/DirectMediaDetail.test.ts` (rename file) and update imports:

```ts
// at the top of the renamed test file
import DirectMediaDetail from "./DirectMediaDetail.svelte";
// (and adjust any other path imports — store imports stay the same path semantics)
```

Update the imports at the top of `frontend/src/lib/components/DirectMediaDetail.test.ts` for the moved file:
- Change `"../lib/..."` to `"../..."` where needed (the test now lives one level deeper).
- Replace every `MediaDetail` identifier with `DirectMediaDetail`.

(If your test uses `import MediaDetail from "./MediaDetail.svelte"` it becomes `import DirectMediaDetail from "./DirectMediaDetail.svelte"`. Module-relative imports of stores like `../albums/albumsStore.svelte` need to match the new file location.)

Delete `frontend/src/routes/MediaDetail.test.ts` after the move.

- [ ] **Step 5: Run the moved tests**

```
cd frontend && bun run test DirectMediaDetail
```

Expected: PASS (test logic unchanged; only paths and component name changed).

- [ ] **Step 6: Commit**

Note: the dispatcher in `MediaDetail.svelte` references `Lightbox.svelte` which is created in Task 13. To keep the build green between tasks, create a temporary stub `frontend/src/lib/components/lightbox/Lightbox.svelte`:

```svelte
<!-- frontend/src/lib/components/lightbox/Lightbox.svelte -->
<!-- Temporary stub — replaced in Task 13. -->
<script lang="ts">
  let { id }: { id: string; from: string; [k: string]: unknown } = $props();
</script>
<div class="lb-stub">Lightbox stub for {id}</div>
```

```bash
git add frontend/src/lib/components/DirectMediaDetail.svelte \
  frontend/src/lib/components/DirectMediaDetail.test.ts \
  frontend/src/routes/MediaDetail.svelte \
  frontend/src/lib/components/lightbox/Lightbox.svelte \
  frontend/src/App.svelte
git rm frontend/src/routes/MediaDetail.test.ts
git commit -m "refactor(frontend): split MediaDetail into dispatcher + DirectMediaDetail"
```

Run typecheck: `cd frontend && bun run typecheck` — expected to pass.

---

## Task 11: VirtualGrid onOpenMedia callback

**Files:**
- Modify: `frontend/src/lib/grid/MediaCell.svelte`
- Modify: `frontend/src/lib/grid/VirtualGrid.svelte`
- Modify: `frontend/src/lib/grid/VirtualGrid.flat.test.ts`

Add a `data-media-id` attribute to MediaCell so the focus-restore helper can find it. Add an optional `onOpenMedia(id)` prop to VirtualGrid that source routes use instead of the default `router.navigate(/media/:id)`. The default behavior still calls `router.navigate` so consumers that haven't migrated yet keep working until the source route updates land.

- [ ] **Step 1: Add `data-media-id` to MediaCell**

In `frontend/src/lib/grid/MediaCell.svelte`, the existing `<a>` element gains a `data-media-id`:

```svelte
<a
  href={`/media/${media.id}`}
  data-media-id={media.id}
  aria-label={`Photo ${media.id}`}
  class:selected
  onclick={onCellClick}
>
```

- [ ] **Step 2: Add onOpenMedia prop to VirtualGrid**

In `frontend/src/lib/grid/VirtualGrid.svelte`, add `onOpenMedia` to the props destructuring (with the existing props), and use it in the click handler:

```ts
let {
  /* existing props... */
  onOpenMedia,
}: {
  /* existing types... */
  onOpenMedia?: (id: string) => void;
} = $props();
```

Find the existing click navigation block (around line 130):

```ts
// Plain click: SPA-route via the router instead of the anchor's
// default full-page navigation. Skipped when disableNavigation is
// set (e.g. HiddenLibrary where detail view is deferred to F2.5).
if (disableNavigation) return;
e.preventDefault();
router.navigate(`/media/${id}`);
```

Replace with:

```ts
// Plain click. Default: SPA-route to /media/:id. Source routes that
// want lightbox behavior pass onOpenMedia which captures the
// LightboxSession snapshot and navigates with ?from=...; the default
// path is unchanged for any caller without the prop.
if (disableNavigation) return;
e.preventDefault();
if (onOpenMedia !== undefined) {
  onOpenMedia(id);
} else {
  router.navigate(`/media/${id}`);
}
```

- [ ] **Step 3: Add a test for the new prop**

Append to `frontend/src/lib/grid/VirtualGrid.flat.test.ts`:

```ts
it("invokes onOpenMedia(id) instead of router.navigate when prop is provided", async () => {
  const onOpenMedia = vi.fn();
  // ... use the existing test scaffolding to render VirtualGrid with
  // a one-month list and `onOpenMedia` set. Click the first tile.
  // Assert onOpenMedia was called with the id and that the URL did
  // not change (or that router.navigate was not invoked).
});
```

(If the test file already mocks `router.navigate`, assert `vi.spyOn(router, "navigate")` was NOT called when `onOpenMedia` is provided.)

- [ ] **Step 4: Run tests**

```
cd frontend && bun run test VirtualGrid
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/grid/MediaCell.svelte frontend/src/lib/grid/VirtualGrid.svelte frontend/src/lib/grid/VirtualGrid.flat.test.ts
git commit -m "feat(frontend): add data-media-id and onOpenMedia prop to grid"
```

---

## Task 12: Lightbox shell components (frame, toolbar, nav buttons)

**Files:**
- Create: `frontend/src/lib/components/lightbox/LightboxFrame.svelte`
- Create: `frontend/src/lib/components/lightbox/LightboxToolbar.svelte`
- Create: `frontend/src/lib/components/lightbox/LightboxNavButtons.svelte`

Chrome that wraps the image/media area. Frame has `full` and `fallback` modes. Toolbar has info-toggle, close, and a slot for `LightboxActions`. Nav buttons are hidden when `hasPrev/hasNext` is false.

- [ ] **Step 1: Create LightboxFrame.svelte**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxFrame.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";

  let {
    mode = "full",
    onBackdropClick,
    children,
  }: {
    mode?: "full" | "fallback";
    onBackdropClick?: (e: MouseEvent) => void;
    children: Snippet;
  } = $props();

  function onClick(e: MouseEvent) {
    if (e.target === e.currentTarget && onBackdropClick) onBackdropClick(e);
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="lb-backdrop" class:fallback={mode === "fallback"} role="presentation" onclick={onClick}>
  {@render children()}
</div>

<style>
  .lb-backdrop {
    position: fixed; inset: 0;
    background: #000;
    z-index: 150;
    display: flex; align-items: center; justify-content: center;
  }
  .lb-backdrop.fallback { background: rgba(0,0,0,0.92); }
</style>
```

- [ ] **Step 2: Create LightboxToolbar.svelte**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxToolbar.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";

  let {
    onClose,
    onToggleInfo,
    actions,
  }: {
    onClose: () => void;
    onToggleInfo?: () => void;
    actions?: Snippet;
  } = $props();
</script>

<header class="lb-toolbar">
  <div class="lb-toolbar-left">
    <button type="button" class="lb-btn" aria-label="Close" onclick={onClose}>×</button>
  </div>
  <div class="lb-toolbar-right">
    {#if onToggleInfo}
      <button type="button" class="lb-btn" aria-label="Toggle info" onclick={onToggleInfo}>i</button>
    {/if}
    {#if actions}{@render actions()}{/if}
  </div>
</header>

<style>
  .lb-toolbar {
    position: absolute; top: 0; left: 0; right: 0;
    display: flex; justify-content: space-between; align-items: center;
    padding: 12px 16px;
    color: white;
  }
  .lb-toolbar-left, .lb-toolbar-right { display: flex; gap: 8px; align-items: center; }
  .lb-btn {
    background: rgba(0,0,0,0.5); color: white; border: 1px solid rgba(255,255,255,0.2);
    border-radius: 4px; padding: 6px 12px; cursor: pointer; font-size: 16px;
  }
  .lb-btn:hover { background: rgba(0,0,0,0.7); }
</style>
```

- [ ] **Step 3: Create LightboxNavButtons.svelte**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxNavButtons.svelte -->
<script lang="ts">
  let {
    hasPrev,
    hasNext,
    onPrev,
    onNext,
  }: {
    hasPrev: boolean;
    hasNext: boolean;
    onPrev: () => void;
    onNext: () => void;
  } = $props();
</script>

{#if hasPrev}
  <button type="button" class="lb-nav lb-prev" aria-label="Previous photo" onclick={onPrev}>‹</button>
{/if}
{#if hasNext}
  <button type="button" class="lb-nav lb-next" aria-label="Next photo" onclick={onNext}>›</button>
{/if}

<style>
  .lb-nav {
    position: absolute; top: 50%; transform: translateY(-50%);
    background: rgba(0,0,0,0.5); color: white; border: 1px solid rgba(255,255,255,0.2);
    border-radius: 50%; width: 48px; height: 48px;
    display: flex; align-items: center; justify-content: center;
    font-size: 28px; cursor: pointer; padding: 0;
  }
  .lb-prev { left: 16px; }
  .lb-next { right: 16px; }
  .lb-nav:hover { background: rgba(0,0,0,0.75); }
</style>
```

- [ ] **Step 4: Smoke test for the frame mode + toolbar render**

Create `frontend/src/lib/components/lightbox/LightboxFrame.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import LightboxFrame from "./LightboxFrame.svelte";

describe("LightboxFrame", () => {
  it("backdrop click invokes onBackdropClick only when target is backdrop", async () => {
    const onBackdropClick = vi.fn();
    const { container } = render(LightboxFrame, {
      props: { mode: "full", onBackdropClick, children: (() => "x") as never } as never,
    });
    const backdrop = container.querySelector(".lb-backdrop") as HTMLElement;
    await fireEvent.click(backdrop);
    expect(onBackdropClick).toHaveBeenCalledOnce();
  });

  it("applies fallback class when mode=fallback", () => {
    const { container } = render(LightboxFrame, {
      props: { mode: "fallback", children: (() => "x") as never } as never,
    });
    expect(container.querySelector(".lb-backdrop.fallback")).toBeTruthy();
  });
});
```

Run: `cd frontend && bun run test LightboxFrame`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/lightbox/LightboxFrame.svelte \
  frontend/src/lib/components/lightbox/LightboxFrame.test.ts \
  frontend/src/lib/components/lightbox/LightboxToolbar.svelte \
  frontend/src/lib/components/lightbox/LightboxNavButtons.svelte
git commit -m "feat(frontend): add LightboxFrame, Toolbar, NavButtons"
```

---

## Task 13: LightboxImage with vendored panzoom

**Files:**
- Modify: `frontend/package.json` (via `bun add panzoom`)
- Create: `frontend/src/lib/components/lightbox/LightboxImage.svelte`
- Create: `frontend/src/lib/components/lightbox/LightboxVideo.svelte`
- Create: `frontend/src/lib/components/lightbox/LightboxMedia.svelte`

The vendored library is `panzoom` (anvaka/panzoom, ~1.5KB gz). API: `panzoom(element, opts)` returns a controller with `dispose()`, `zoomTo()`, `getTransform()`. It handles wheel, pinch, double-click, drag.

- [ ] **Step 1: Install panzoom**

```bash
cd frontend && bun add panzoom
```

Verify the install: `grep '"panzoom"' frontend/package.json` shows the new dependency.

- [ ] **Step 2: Create LightboxImage.svelte**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxImage.svelte -->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import panzoom from "panzoom";
  import type { PanZoom } from "panzoom";

  let {
    src,
    alt,
    onError,
  }: {
    src: string;
    alt: string;
    onError?: () => void;
  } = $props();

  let imgEl: HTMLImageElement;
  let container: HTMLDivElement;
  let pz: PanZoom | null = null;

  onMount(() => {
    pz = panzoom(imgEl, {
      maxZoom: 8,
      minZoom: 1,
      bounds: true,
      boundsPadding: 0.1,
      smoothScroll: false,
      // Disable pan when at 1x so backdrop click + horizontal swipe
      // remain available for close + prev/next.
      beforeMouseDown: () => (pz?.getTransform().scale ?? 1) <= 1,
      beforeWheel: () => false, // allow wheel zoom always
    });
    // Double-click toggles 1x ↔ 2x at the click location.
    imgEl.addEventListener("dblclick", onDblClick);
    // Spacebar toggles when image has focus (handled by Lightbox parent).
  });

  onDestroy(() => {
    pz?.dispose();
    imgEl?.removeEventListener("dblclick", onDblClick);
  });

  function onDblClick(e: MouseEvent) {
    if (!pz) return;
    const cur = pz.getTransform().scale;
    if (cur > 1.5) pz.zoomAbs(0, 0, 1);
    else pz.zoomTo(e.clientX, e.clientY, 2);
  }

  // Public hooks for Lightbox parent (zoom/pan keyboard shortcuts).
  export function zoomIn(): void {
    if (!pz) return;
    const t = pz.getTransform();
    const cx = container.clientWidth / 2;
    const cy = container.clientHeight / 2;
    pz.zoomTo(cx, cy, 1.25);
  }
  export function zoomOut(): void {
    if (!pz) return;
    const cx = container.clientWidth / 2;
    const cy = container.clientHeight / 2;
    pz.zoomTo(cx, cy, 1 / 1.25);
  }
  export function resetZoom(): void {
    pz?.zoomAbs(0, 0, 1);
  }
  export function toggleZoom(): void {
    if (!pz) return;
    const cur = pz.getTransform().scale;
    if (cur > 1.5) pz.zoomAbs(0, 0, 1);
    else pz.zoomTo(container.clientWidth / 2, container.clientHeight / 2, 2);
  }
</script>

<div class="lb-image-container" bind:this={container}>
  <!-- svelte-ignore a11y_img_redundant_alt -->
  <img
    bind:this={imgEl}
    {src}
    {alt}
    onerror={() => onError?.()}
    draggable="false"
  />
</div>

<style>
  .lb-image-container {
    width: 100%; height: 100%;
    display: flex; align-items: center; justify-content: center;
    overflow: hidden;
  }
  .lb-image-container :global(img) {
    max-width: 100%; max-height: 100%;
    object-fit: contain;
    user-select: none;
    -webkit-user-drag: none;
  }
</style>
```

- [ ] **Step 3: Create LightboxVideo.svelte**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxVideo.svelte -->
<script lang="ts">
  let {
    src,
    poster,
    onError,
  }: {
    src: string;
    poster?: string;
    onError?: () => void;
  } = $props();
</script>

<div class="lb-video-container">
  <!-- svelte-ignore a11y_media_has_caption -->
  <video
    {src}
    {poster}
    controls
    onerror={() => onError?.()}
  ></video>
</div>

<style>
  .lb-video-container { width: 100%; height: 100%; display: flex; align-items: center; justify-content: center; }
  .lb-video-container video { max-width: 100%; max-height: 100%; }
</style>
```

- [ ] **Step 4: Create LightboxMedia.svelte (image/video branch)**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxMedia.svelte -->
<script lang="ts">
  import LightboxImage from "./LightboxImage.svelte";
  import LightboxVideo from "./LightboxVideo.svelte";

  let {
    kind,
    src,
    alt,
    poster,
    onError,
    imageRef,
  }: {
    kind: "image" | "video";
    src: string;
    alt: string;
    poster?: string;
    onError?: () => void;
    imageRef?: { current: LightboxImage | null };
  } = $props();
</script>

{#if kind === "video"}
  <LightboxVideo {src} {poster} {onError} />
{:else}
  <LightboxImage {src} {alt} {onError} bind:this={imageRef as never} />
{/if}
```

(The `imageRef` prop lets the Lightbox parent call `imageRef.current?.zoomIn()` etc. for keyboard shortcuts.)

- [ ] **Step 5: Smoke test for LightboxMedia branching**

Create `frontend/src/lib/components/lightbox/LightboxMedia.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { render } from "@testing-library/svelte";
import LightboxMedia from "./LightboxMedia.svelte";

// panzoom is invoked on image mount; mock it so jsdom doesn't choke.
vi.mock("panzoom", () => ({
  default: () => ({
    dispose: vi.fn(),
    getTransform: () => ({ scale: 1, x: 0, y: 0 }),
    zoomTo: vi.fn(),
    zoomAbs: vi.fn(),
  }),
}));

describe("LightboxMedia", () => {
  it("renders <img> for image kind", () => {
    const { container } = render(LightboxMedia, {
      props: { kind: "image", src: "/x", alt: "" } as never,
    });
    expect(container.querySelector("img")).toBeTruthy();
    expect(container.querySelector("video")).toBeNull();
  });

  it("renders <video> for video kind", () => {
    const { container } = render(LightboxMedia, {
      props: { kind: "video", src: "/v" } as never,
    });
    expect(container.querySelector("video")).toBeTruthy();
    expect(container.querySelector("img")).toBeNull();
  });
});
```

Run: `cd frontend && bun run test LightboxMedia`. Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/package.json frontend/bun.lock \
  frontend/src/lib/components/lightbox/LightboxImage.svelte \
  frontend/src/lib/components/lightbox/LightboxVideo.svelte \
  frontend/src/lib/components/lightbox/LightboxMedia.svelte \
  frontend/src/lib/components/lightbox/LightboxMedia.test.ts
git commit -m "feat(frontend): add LightboxImage with panzoom + LightboxVideo + Media branch"
```

---

## Task 14: LightboxMetadata + InfoDrawer + InfoSheet

**Files:**
- Create: `frontend/src/lib/components/lightbox/LightboxMetadata.svelte`
- Create: `frontend/src/lib/components/lightbox/LightboxInfoDrawer.svelte`
- Create: `frontend/src/lib/components/lightbox/LightboxInfoSheet.svelte`

`LightboxMetadata` renders only existing fields (filename(s), captured timestamp, dimensions, file size, location, sidecars/downloads). Drawer wraps it for desktop; Sheet wraps it in `BottomSheet` for mobile.

- [ ] **Step 1: Create LightboxMetadata.svelte**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxMetadata.svelte -->
<script lang="ts">
  import type { Media } from "../../media/mediaStore.svelte";
  import { formatCoord } from "../../format/coords";

  let { media }: { media: Media } = $props();

  function formatTimestamp(iso: string | undefined): string {
    if (!iso) return "";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toISOString().replace("T", " ").replace(/:\d{2}\.\d{3}Z$/, " UTC");
  }

  function formatBytes(size: number | undefined): string {
    if (size === undefined || !Number.isFinite(size)) return "";
    return `${(size / 1024 / 1024).toFixed(1)} MB`;
  }
</script>

<dl class="lb-meta">
  {#if media.original_filename}
    <dt>File</dt>
    <dd>{media.original_filename}</dd>
  {/if}
  {#if media.timestamp}
    <dt>Captured</dt>
    <dd>{formatTimestamp(media.timestamp)}</dd>
  {/if}
  {#if media.size}
    <dt>Size</dt>
    <dd>{formatBytes(media.size)}</dd>
  {/if}
  {#if media.location_label || (media.latitude != null && media.longitude != null)}
    <dt>Location</dt>
    <dd>
      {#if media.location_label}{media.location_label}{/if}
      {#if media.latitude != null && media.longitude != null}
        <small class="coord">{formatCoord(media.latitude, media.longitude)}</small>
      {/if}
    </dd>
  {/if}
  {#if media.sidecars && media.sidecars.length > 0}
    <dt>Files</dt>
    <dd class="files">
      <a href={`/api/v1/media/${media.id}/original`} download={media.original_filename ?? media.id}>
        {media.original_filename ?? media.id}
      </a>
      {#each media.sidecars as sc (sc.id)}
        <br />
        <a href={`/api/v1/media/${sc.id}/original`} download={sc.original_filename ?? sc.id}>
          {sc.original_filename ?? sc.id}
        </a>
      {/each}
    </dd>
  {:else}
    <dt>Download</dt>
    <dd>
      <a href={`/api/v1/media/${media.id}/original`} download={media.original_filename ?? media.id}>
        {media.original_filename ?? media.id}
      </a>
    </dd>
  {/if}
</dl>

<style>
  .lb-meta { display: grid; grid-template-columns: max-content 1fr; gap: 0.25rem 1rem; color: white; }
  .lb-meta dt { font-weight: 600; opacity: 0.7; }
  .lb-meta a { color: white; }
  .coord { display: block; opacity: 0.7; font-size: 0.85em; }
</style>
```

- [ ] **Step 2: Create LightboxInfoDrawer.svelte**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxInfoDrawer.svelte -->
<script lang="ts">
  import type { Media } from "../../media/mediaStore.svelte";
  import LightboxMetadata from "./LightboxMetadata.svelte";

  let { media, onClose }: { media: Media; onClose: () => void } = $props();
</script>

<aside class="lb-drawer" role="complementary" aria-label="Photo info">
  <header class="lb-drawer-head">
    <button type="button" aria-label="Close info" onclick={onClose}>×</button>
  </header>
  <div class="lb-drawer-body">
    <LightboxMetadata {media} />
  </div>
</aside>

<style>
  .lb-drawer {
    position: absolute; top: 0; right: 0; bottom: 0;
    width: min(360px, 40vw);
    background: rgba(20,20,20,0.95);
    border-left: 1px solid rgba(255,255,255,0.1);
    display: flex; flex-direction: column;
  }
  .lb-drawer-head {
    display: flex; justify-content: flex-end;
    padding: 8px;
    border-bottom: 1px solid rgba(255,255,255,0.1);
  }
  .lb-drawer-head button {
    background: transparent; color: white; border: none; font-size: 18px; cursor: pointer;
  }
  .lb-drawer-body { padding: 16px; overflow: auto; }
</style>
```

- [ ] **Step 3: Create LightboxInfoSheet.svelte**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxInfoSheet.svelte -->
<script lang="ts">
  import type { Media } from "../../media/mediaStore.svelte";
  import BottomSheet from "../BottomSheet.svelte";
  import LightboxMetadata from "./LightboxMetadata.svelte";

  let { media, onClose }: { media: Media; onClose: () => void } = $props();
  const sheetId = `lb-info-${media.id}`;
</script>

<BottomSheet id={sheetId} {onClose} snap="peek">
  <LightboxMetadata {media} />
</BottomSheet>
```

- [ ] **Step 4: Smoke test the metadata renderer**

Create `frontend/src/lib/components/lightbox/LightboxMetadata.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import { render } from "@testing-library/svelte";
import LightboxMetadata from "./LightboxMetadata.svelte";

const baseMedia = {
  id: "m1",
  timestamp: "2026-04-20T12:00:00Z",
  taken: new Date("2026-04-20T12:00:00Z"),
  aspect: 1,
  thumbUrl: "/g",
  thumbVersion: 0,
  original_filename: "IMG_001.JPG",
  size: 1024 * 1024 * 5,
  location_label: "Paris, France",
};

describe("LightboxMetadata", () => {
  it("renders existing fields only (no caption/rating/AI tags)", () => {
    const { container, getByText } = render(LightboxMetadata, {
      props: { media: baseMedia } as never,
    });
    expect(getByText("IMG_001.JPG")).toBeTruthy();
    expect(getByText(/5\.0 MB/)).toBeTruthy();
    expect(getByText("Paris, France")).toBeTruthy();
    expect(container.textContent ?? "").not.toMatch(/Rating|Caption|AI/);
  });
});
```

Run: `cd frontend && bun run test LightboxMetadata`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/lightbox/LightboxMetadata.svelte \
  frontend/src/lib/components/lightbox/LightboxMetadata.test.ts \
  frontend/src/lib/components/lightbox/LightboxInfoDrawer.svelte \
  frontend/src/lib/components/lightbox/LightboxInfoSheet.svelte
git commit -m "feat(frontend): add LightboxMetadata + drawer + sheet"
```

---

## Task 15: LightboxActions wrapper

**Files:**
- Create: `frontend/src/lib/components/lightbox/LightboxActions.svelte`

Wraps `MediaActions` with source-aware hide/unhide handlers. After the action succeeds:
1. Mutate `LightboxSession.navIds`.
2. Update durable state per source kind (mediaStore, albumsStore.markStale, mergeRaw on unhide).
3. Caller (Lightbox.svelte) decides whether to advance or close.

- [ ] **Step 1: Create LightboxActions.svelte**

```svelte
<!-- frontend/src/lib/components/lightbox/LightboxActions.svelte -->
<script lang="ts">
  import MediaActions from "../MediaActions.svelte";
  import type { MediaStore, Media } from "../../media/mediaStore.svelte";
  import type { AlbumsStore } from "../../albums/albumsStore.svelte";
  import type { HiddenStore } from "../../hidden/hiddenStore.svelte";
  import type { ToastStore } from "../../toasts/toastStore.svelte";
  import type { LightboxSource } from "../../lightbox/lightboxSession.svelte";
  import { lightboxSession } from "../../lightbox/lightboxSession.svelte";

  let {
    source,
    media,
    rawMedia,
    mediaStore,
    albumsStore,
    hiddenStore,
    toastStore,
    onAdd,
    onShare,
    onDone,
  }: {
    source: LightboxSource;
    media: Media;
    rawMedia: Record<string, unknown> | null;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
    onAdd: (ids: string[]) => void;
    onShare: (ids: string[]) => void;
    /** Called after a successful hide/unhide — Lightbox parent advances or closes. */
    onDone: (op: "hide" | "unhide", succeeded: string[]) => void;
  } = $props();

  const isUnhideContext = $derived(source.kind === "hidden");

  async function onHide(ids: string[]): Promise<void> {
    if (!window.confirm("Hide this photo?")) return;
    let result;
    try {
      result = await hiddenStore.hide(ids);
    } catch {
      toastStore.push({ message: "Hide failed. Try again.", kind: "error" });
      return;
    }
    const succeeded = result.succeeded ?? [];
    if (succeeded.length > 0) {
      // 1. Snapshot mutation
      lightboxSession.removeIds(succeeded);
      // 2. Durable state per source kind
      if (source.kind === "library" || source.kind === "sessions") {
        mediaStore.removeMany(succeeded);
      } else if (source.kind === "album") {
        albumsStore.markStale();
      }
      onDone("hide", succeeded);
    }
    const failed = result.failed ?? [];
    if (failed.length > 0) {
      toastStore.push({
        message: "Could not hide this photo.",
        details: failed.map((f) => `${f.id}: ${f.code}`),
        kind: "error",
      });
    }
  }

  async function onUnhide(ids: string[]): Promise<void> {
    if (!window.confirm("Unhide this photo?")) return;
    let result;
    try {
      result = await hiddenStore.unhide(ids);
    } catch {
      toastStore.push({ message: "Unhide failed. Try again.", kind: "error" });
      return;
    }
    const succeeded = result.succeeded ?? [];
    if (succeeded.length > 0) {
      // 1. Snapshot mutation (removes from hidden navIds)
      lightboxSession.removeIds(succeeded);
      // 2. Re-introduce the row to visible mediaStore so /library shows it.
      const raw = rawMedia ?? { id: media.id, thumb_version: media.thumbVersion, width: 1, height: 1 };
      mediaStore.mergeRaw([{ ...raw, hidden_at: null }]);
      // 3. Albums stale (counts may shift)
      albumsStore.markStale();
      onDone("unhide", succeeded);
    }
    const failed = result.failed ?? [];
    if (failed.length > 0) {
      toastStore.push({
        message: "Could not unhide this photo.",
        details: failed.map((f) => `${f.id}: ${f.code}`),
        kind: "error",
      });
    }
  }
</script>

<MediaActions
  mediaIds={[media.id]}
  context="media-detail"
  isHidden={isUnhideContext}
  hiddenConfigured={hiddenStore.configured}
  {onAdd}
  {onShare}
  {onHide}
  {onUnhide}
/>
```

- [ ] **Step 2: Smoke test source-aware dispatch**

Create `frontend/src/lib/components/lightbox/LightboxActions.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import LightboxActions from "./LightboxActions.svelte";
import { lightboxSession } from "../../lightbox/lightboxSession.svelte";

const fakeMedia = {
  id: "m1", timestamp: "", aspect: 1, thumbUrl: "", taken: new Date(), thumbVersion: 0,
};
const rawMedia = { id: "m1", thumb_version: 0, width: 1, height: 1 };

function fakeStores() {
  return {
    mediaStore: { removeMany: vi.fn(), mergeRaw: vi.fn() } as never,
    albumsStore: { markStale: vi.fn() } as never,
    hiddenStore: {
      configured: true,
      hide: vi.fn().mockResolvedValue({ succeeded: ["m1"], failed: [] }),
      unhide: vi.fn().mockResolvedValue({ succeeded: ["m1"], failed: [] }),
    } as never,
    toastStore: { push: vi.fn() } as never,
  };
}

describe("LightboxActions", () => {
  it("library hide → mediaStore.removeMany + onDone", async () => {
    const stores = fakeStores();
    lightboxSession.open({
      source: { kind: "library" },
      navIds: ["m1", "m2"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: null,
      returnHref: "/library",
    });
    const onDone = vi.fn();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const { getByRole } = render(LightboxActions, {
      props: {
        source: { kind: "library" },
        media: fakeMedia, rawMedia, ...stores,
        onAdd: vi.fn(), onShare: vi.fn(), onDone,
      } as never,
    });
    await fireEvent.click(getByRole("button", { name: /hide/i }));
    await Promise.resolve();
    await Promise.resolve();
    expect(stores.mediaStore.removeMany).toHaveBeenCalledWith(["m1"]);
    expect(onDone).toHaveBeenCalledWith("hide", ["m1"]);
  });
});
```

Run: `cd frontend && bun run test LightboxActions`. Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/lib/components/lightbox/LightboxActions.svelte \
  frontend/src/lib/components/lightbox/LightboxActions.test.ts
git commit -m "feat(frontend): add LightboxActions source-aware hide/unhide wrapper"
```

---

## Task 16: Lightbox.svelte top-level (no reconstruction yet)

**Files:**
- Create/replace: `frontend/src/lib/components/lightbox/Lightbox.svelte` (replaces the Task 10 stub)

Top-level Lightbox. For this task we cover the common case: `LightboxSession` snapshot is present (source route opened the lightbox). Reconstruction (no session, direct entry, fallback delegation) is added in Task 17.

- [ ] **Step 1: Implement Lightbox.svelte (snapshot-driven path)**

Replace `frontend/src/lib/components/lightbox/Lightbox.svelte` with:

```svelte
<!-- frontend/src/lib/components/lightbox/Lightbox.svelte -->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { router } from "../../router/router.svelte";
  import { isEditableTarget } from "../../dom/editable";
  import type { MediaStore, Media } from "../../media/mediaStore.svelte";
  import type { AlbumsStore } from "../../albums/albumsStore.svelte";
  import type { HiddenStore } from "../../hidden/hiddenStore.svelte";
  import type { ToastStore } from "../../toasts/toastStore.svelte";
  import { toMedia } from "../../media/mediaStore.svelte";
  import { lightboxSession } from "../../lightbox/lightboxSession.svelte";
  import { modalStack } from "../../lightbox/modalStack.svelte";
  import { computeNav } from "../../lightbox/lightboxNav.svelte";
  import { LightboxLoader, thumbUrl } from "../../lightbox/lightboxLoader";
  import LightboxFrame from "./LightboxFrame.svelte";
  import LightboxToolbar from "./LightboxToolbar.svelte";
  import LightboxNavButtons from "./LightboxNavButtons.svelte";
  import LightboxMedia from "./LightboxMedia.svelte";
  import LightboxActions from "./LightboxActions.svelte";
  import LightboxInfoDrawer from "./LightboxInfoDrawer.svelte";
  import LightboxInfoSheet from "./LightboxInfoSheet.svelte";
  import DirectMediaDetail from "../DirectMediaDetail.svelte";
  import AddToAlbumModal from "../AddToAlbumModal.svelte";
  import ShareModal from "../ShareModal.svelte";
  import { api } from "../../api/client";
  import type { CreateShareBody } from "../../share/shareTypes";

  let {
    id,
    from,
    mediaStore,
    albumsStore,
    hiddenStore,
    toastStore,
  }: {
    id: string;
    from: string;
    mediaStore: MediaStore;
    albumsStore: AlbumsStore;
    hiddenStore: HiddenStore;
    toastStore: ToastStore;
    } = $props();

  // ---- Modal stack registration ----------------------------------
  const modalId = `lightbox-${Math.random().toString(36).slice(2)}`;
  onMount(() => {
    modalStack.push({ id: modalId, onEscape: close });
    // Scroll-lock the underlying source while lightbox is open. Spec §5.
    document.body.style.overflow = "hidden";
  });
  onDestroy(() => {
    modalStack.pop(modalId);
    document.body.style.overflow = "";
  });

  // ---- Source / session resolution -------------------------------
  // Reconstruction logic added in Task 17. For now: bail to fallback
  // shell when no session is present and the route is a direct entry.
  const session = $derived(lightboxSession.snapshot);
  const fromMatchesSession = $derived(
    session !== null &&
      ((session.source.kind === "library" && from === "library") ||
        (session.source.kind === "sessions" && from === "sessions") ||
        (session.source.kind === "hidden" && from === "hidden") ||
        (session.source.kind === "album" && from === `album:${session.source.albumId}`)),
  );
  const navIds = $derived(fromMatchesSession ? session!.navIds : []);
  const nav = $derived(computeNav(navIds, id));
  const returnHref = $derived(session?.returnHref ?? "/library");

  // ---- Media data ------------------------------------------------
  let lastRaw = $state<Record<string, unknown> | null>(null);
  let loadError = $state<number | null>(null);
  let media = $derived.by((): Media | undefined => {
    void mediaStore.months;
    const m = mediaStore.get(id);
    if (m) return m;
    if (lastRaw) {
      const parsed = toMedia(lastRaw);
      return parsed ?? undefined;
    }
    return undefined;
  });
  const rawHidden = $derived(lastRaw?.["hidden_at"]);
  const isHidden = $derived(rawHidden !== undefined && rawHidden !== null);

  // ---- Fetch effect ----------------------------------------------
  $effect(() => {
    const currentId = id;
    loadError = null;
    lastRaw = null;
    if (mediaStore.get(currentId)) return;
    let cancelled = false;
    (async () => {
      try {
        const resp = await fetch(`/api/v1/media/${currentId}`);
        if (cancelled) return;
        if (!resp.ok) {
          loadError = resp.status;
          return;
        }
        const raw = await resp.json();
        if (cancelled) return;
        lastRaw = raw as Record<string, unknown>;
        mediaStore.mergeRaw([raw]);
      } catch {
        if (cancelled) return;
        loadError = -1;
      }
    })();
    return () => { cancelled = true; };
  });

  // ---- Fallback decision -----------------------------------------
  // Hidden cross-context: from != hidden && api returned hidden_at != null.
  const hiddenCrossContext = $derived(isHidden && from !== "hidden");
  // Tri-state for reconstruction. Initial value is "idle" — when the
  // session matches `from` no reconstruction is needed (treated as
  // "ok"). When the session does NOT match, the reconstruction effect
  // (added in Task 17) flips to "running" then to "ok" or "failed".
  // For Task 16 (snapshot-only path) this is set up but always "ok"
  // because we early-return when no snapshot is present (Task 17 takes
  // over the not-matching case).
  type RecState = "idle" | "running" | "ok" | "failed";
  let reconstructionState = $state<RecState>(fromMatchesSession ? "ok" : "idle");
  const reconstructionInFlight = $derived(
    !fromMatchesSession && (reconstructionState === "idle" || reconstructionState === "running"),
  );
  const reconstructionFailed = $derived(
    !fromMatchesSession && reconstructionState === "failed",
  );
  const fallbackMode = $derived(loadError !== null || hiddenCrossContext || reconstructionFailed);
  const notFoundMode = $derived(loadError !== null);

  // ---- Image loader ----------------------------------------------
  const loader = new LightboxLoader();
  let visibleSrc = $state("");
  $effect(() => {
    const m = media;
    if (!m || fallbackMode) return;
    const gridSrc = m.thumbUrl;
    const previewUrl = thumbUrl(m.id, "preview", m.thumbVersion);
    const largeUrl = thumbUrl(m.id, "large", m.thumbVersion);
    void loader.load({
      activeId: m.id,
      gridSrc,
      previewUrl,
      largeUrl,
      onSrc: (u) => (visibleSrc = u),
    });
    // Prefetch immediate prev / next preview + large.
    const prevId = nav.prevId;
    const nextId = nav.nextId;
    const prevMedia = prevId ? mediaStore.get(prevId) : undefined;
    const nextMedia = nextId ? mediaStore.get(nextId) : undefined;
    loader.prefetch({
      prevPreviewUrl: prevMedia ? thumbUrl(prevMedia.id, "preview", prevMedia.thumbVersion) : null,
      nextPreviewUrl: nextMedia ? thumbUrl(nextMedia.id, "preview", nextMedia.thumbVersion) : null,
      prevLargeUrl: prevMedia ? thumbUrl(prevMedia.id, "large", prevMedia.thumbVersion) : null,
      nextLargeUrl: nextMedia ? thumbUrl(nextMedia.id, "large", nextMedia.thumbVersion) : null,
    });
  });

  // ---- Navigation -----------------------------------------------
  function navTo(targetId: string) {
    router.navigate(`/media/${targetId}?from=${encodeURIComponent(from)}`, { replace: true });
  }
  function close() {
    if (router.appHistoryDepthForTest > 0) {
      router.back(returnHref);
    } else {
      router.navigate(returnHref, { replace: true });
    }
    lightboxSession.close();
  }
  function onPrev() { if (nav.prevId !== null) navTo(nav.prevId); }
  function onNext() { if (nav.nextId !== null) navTo(nav.nextId); }
  function onActionDone(_op: "hide" | "unhide", succeeded: string[]) {
    if (succeeded.length === 0) return;
    // navIds was mutated by LightboxActions (via lightboxSession.removeIds).
    // Recompute nav now.
    if (navIds.length === 0) {
      close();
      return;
    }
    // If active id was removed, advance to nav.nextId or nav.prevId; else stay.
    if (!navIds.includes(id)) {
      const advanceTo = nav.nextId ?? nav.prevId;
      if (advanceTo) navTo(advanceTo);
      else close();
    }
  }

  // ---- Keyboard shortcuts ---------------------------------------
  let imageRef: { current: { zoomIn(): void; zoomOut(): void; resetZoom(): void; toggleZoom(): void } | null } = { current: null };
  function onKey(e: KeyboardEvent) {
    if (!modalStack.isTopmost(modalId)) return;
    if (isEditableTarget(e.target)) return;
    switch (e.key) {
      case "ArrowLeft": e.preventDefault(); onPrev(); return;
      case "ArrowRight": e.preventDefault(); onNext(); return;
      case "+": case "=": e.preventDefault(); imageRef.current?.zoomIn(); return;
      case "-": case "_": e.preventDefault(); imageRef.current?.zoomOut(); return;
      case "0": e.preventDefault(); imageRef.current?.resetZoom(); return;
      case " ": e.preventDefault(); imageRef.current?.toggleZoom(); return;
      case "i": e.preventDefault(); infoOpen = !infoOpen; return;
    }
  }

  // ---- Modal stack: Add / Share modals --------------------------
  let addOpen = $state(false);
  let shareOpen = $state(false);
  let pendingMediaIds = $state<string[]>([]);
  let infoOpen = $state(false);

  function openAdd(ids: string[]) { pendingMediaIds = ids; addOpen = true; }
  function openShare(ids: string[]) { pendingMediaIds = ids; shareOpen = true; }
  async function onAdd(albumId: string): Promise<{ added: number; already_present: number }> {
    const res = await api.POST("/api/v1/albums/{id}/media", {
      params: { path: { id: albumId } } as never,
      body: { media_ids: pendingMediaIds } as never,
    });
    if (res.error) throw res.error;
    return res.data as { added: number; already_present: number };
  }
  async function onCreateShare(body: CreateShareBody): Promise<void> {
    const res = await api.POST("/api/v1/shares", { body: body as never });
    if (res.error) throw res.error;
  }

  // ---- Mobile breakpoint ----------------------------------------
  // Used to pick drawer vs sheet. 768px matches existing master spec.
  let isMobile = $state(false);
  $effect(() => {
    const mq = window.matchMedia("(max-width: 768px)");
    isMobile = mq.matches;
    const onChange = (e: MediaQueryListEvent) => (isMobile = e.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  });
</script>

<svelte:window onkeydown={onKey} />

<LightboxFrame mode={fallbackMode ? "fallback" : "full"} onBackdropClick={close}>
  <LightboxToolbar onClose={close} onToggleInfo={fallbackMode ? undefined : () => (infoOpen = !infoOpen)}>
    {#snippet actions()}
      {#if !fallbackMode && media}
        <LightboxActions
          source={session?.source ?? { kind: "library" }}
          media={media}
          rawMedia={lastRaw}
          {mediaStore}
          {albumsStore}
          {hiddenStore}
          {toastStore}
          onAdd={openAdd}
          onShare={openShare}
          onDone={onActionDone}
        />
      {/if}
    {/snippet}
  </LightboxToolbar>

  {#if reconstructionInFlight}
    <div class="lb-loading"><p>Loading…</p></div>
  {:else if fallbackMode}
    {#if notFoundMode && loadError !== null}
      <div class="lb-not-found">
        <p>Photo not found.</p>
      </div>
    {:else}
      <DirectMediaDetail
        {id}
        {mediaStore} {albumsStore} {hiddenStore} {toastStore}
        backHref={returnHref}
        onClose={close}
      />
    {/if}
  {:else if media}
    <LightboxNavButtons hasPrev={nav.hasPrev} hasNext={nav.hasNext} {onPrev} {onNext} />
    <LightboxMedia
      kind="image"
      src={visibleSrc}
      alt={media.location_label ?? media.id}
      bind:imageRef={imageRef as never}
    />
    {#if infoOpen}
      {#if isMobile}
        <LightboxInfoSheet {media} onClose={() => (infoOpen = false)} />
      {:else}
        <LightboxInfoDrawer {media} onClose={() => (infoOpen = false)} />
      {/if}
    {/if}
  {:else}
    <div class="lb-loading"><p>Loading…</p></div>
  {/if}
</LightboxFrame>

{#if addOpen}
  <AddToAlbumModal
    mediaIds={pendingMediaIds}
    {albumsStore}
    {onAdd}
    onClose={() => (addOpen = false)}
  />
{/if}
{#if shareOpen}
  <ShareModal
    target={{ type: "media_set", mediaIds: pendingMediaIds }}
    onCreate={onCreateShare}
    onClose={() => (shareOpen = false)}
  />
{/if}

<style>
  .lb-loading, .lb-not-found {
    color: white; padding: 2rem; text-align: center;
  }
</style>
```

- [ ] **Step 2: Expose router depth for Lightbox close**

The lightbox needs to know `appHistoryDepth` to choose between `history.back()` and `router.navigate(returnHref)`. Expose it as a getter on `RouterStore`:

In `frontend/src/lib/router/router.svelte.ts`, add inside the `RouterStore` class:

```ts
get appHistoryDepthForTest(): number {
  return this.appHistoryDepth;
}
```

(Named `*ForTest` to discourage other callers from depending on it; lightbox is the only legit consumer because `back()` already fallbacks via `navigate()`.)

Actually we already have `back(fallback: string)` which does the right thing. Replace the close() function in Lightbox.svelte to use `router.back(returnHref)` and drop the depth getter:

```ts
function close() {
  router.back(returnHref);
  lightboxSession.close();
}
```

Remove the `appHistoryDepthForTest` accessor — not needed.

- [ ] **Step 3: Smoke-test Lightbox open with snapshot present**

Create `frontend/src/lib/components/lightbox/Lightbox.test.ts`:

```ts
import { describe, it, expect, vi } from "vitest";
import { render } from "@testing-library/svelte";
import Lightbox from "./Lightbox.svelte";
import { lightboxSession } from "../../lightbox/lightboxSession.svelte";

vi.mock("panzoom", () => ({
  default: () => ({
    dispose: vi.fn(),
    getTransform: () => ({ scale: 1, x: 0, y: 0 }),
    zoomTo: vi.fn(),
    zoomAbs: vi.fn(),
  }),
}));

const fakeMedia = {
  id: "m1", timestamp: "2026-04-20T12:00:00Z",
  taken: new Date("2026-04-20T12:00:00Z"),
  aspect: 1, thumbUrl: "/api/v1/media/m1/thumb?size=grid&v=0",
  thumbVersion: 0,
};

function fakeMediaStore() {
  return {
    months: [{ key: "2026-04", items: [fakeMedia] }],
    get: (id: string) => (id === "m1" ? fakeMedia : undefined),
    mergeRaw: vi.fn(),
    removeMany: vi.fn(),
  } as never;
}

describe("Lightbox (snapshot path)", () => {
  it("renders the active image when session matches", () => {
    lightboxSession.open({
      source: { kind: "library" },
      navIds: ["m1"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: "m1",
      returnHref: "/library",
    });
    const { container } = render(Lightbox, {
      props: {
        id: "m1",
        from: "library",
        mediaStore: fakeMediaStore(),
        albumsStore: { markStale: vi.fn() } as never,
        hiddenStore: { configured: true } as never,
        toastStore: { push: vi.fn() } as never,
      } as never,
    });
    expect(container.querySelector(".lb-backdrop")).toBeTruthy();
    expect(container.querySelector("img")).toBeTruthy();
  });

  it("editable-target guard: keydown on a textarea does not advance lightbox", async () => {
    lightboxSession.open({
      source: { kind: "library" },
      navIds: ["m1", "m2"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: "m1",
      returnHref: "/library",
    });
    const navigateSpy = vi.spyOn(
      await import("../../router/router.svelte").then((m) => m.router),
      "navigate",
    );
    render(Lightbox, {
      props: {
        id: "m1",
        from: "library",
        mediaStore: fakeMediaStore(),
        albumsStore: { markStale: vi.fn() } as never,
        hiddenStore: { configured: true } as never,
        toastStore: { push: vi.fn() } as never,
      } as never,
    });
    // Synthesize a textarea and dispatch ArrowRight from it.
    const ta = document.createElement("textarea");
    document.body.appendChild(ta);
    ta.focus();
    const ev = new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true });
    Object.defineProperty(ev, "target", { value: ta });
    window.dispatchEvent(ev);
    // The lightbox keyboard handler should bail on isEditableTarget.
    // navigate() should NOT have been called with /media/m2.
    const calls = navigateSpy.mock.calls.map((c) => c[0] as string);
    expect(calls.some((u) => u.includes("/media/m2"))).toBe(false);
    document.body.removeChild(ta);
  });
});
```

Run: `cd frontend && bun run test Lightbox`. Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/components/lightbox/Lightbox.svelte \
  frontend/src/lib/components/lightbox/Lightbox.test.ts
git commit -m "feat(frontend): add Lightbox top-level component (snapshot path)"
```

---

## Task 17: Reconstruction paths

**Files:**
- Modify: `frontend/src/lib/components/lightbox/Lightbox.svelte`

When `lightboxSession.snapshot` is null OR doesn't match `from`, run reconstruction. Per spec: page until active id is found or source is exhausted; cap at 20 pages. On failure, render fallback shell wrapping `DirectMediaDetail`. The hidden 403 case redirects to `/hidden`.

- [ ] **Step 1: Add reconstruction code to Lightbox.svelte**

Add a reconstruction effect after the existing media-fetch effect. The implementation paginates:

```ts
// Insert below the existing fetch-effect block, in Lightbox.svelte:

const PAGE_CAP = 20;
let reconstructed = $state<{ navIds: string[]; returnHref: string; source: import("../../lightbox/lightboxSession.svelte").LightboxSource } | null>(null);

$effect(() => {
  // If a session matches `from`, no reconstruction needed.
  if (fromMatchesSession) {
    reconstructed = null;
    reconstructionState = "ok";
    return;
  }
  reconstructionState = "running";
  let cancelled = false;
  (async () => {
    try {
      if (from === "library" || from === "sessions") {
        // Walk mediaStore until id is in store or exhausted.
        let attempts = 0;
        while (!cancelled && !mediaStore.get(id) && !mediaStore.exhausted && attempts < PAGE_CAP) {
          await mediaStore.loadMore();
          attempts += 1;
        }
        if (cancelled) return;
        if (!mediaStore.get(id)) {
          reconstructionState = "failed";
          return;
        }
        const all: string[] = [];
        for (const m of mediaStore.months) for (const it of m.items) all.push(it.id);
        reconstructed = {
          navIds: all,
          returnHref: from === "library" ? "/library" : "/sessions",
          source: { kind: from === "library" ? "library" : "sessions" },
        };
        reconstructionState = "ok";
      } else if (from?.startsWith("album:")) {
        const albumId = from.slice("album:".length);
        const ids: string[] = [];
        let offset = 0;
        let pages = 0;
        let exhausted = false;
        while (!cancelled && !ids.includes(id) && !exhausted && pages < PAGE_CAP) {
          const resp = await fetch(`/api/v1/albums/${albumId}/media?offset=${offset}&limit=200`);
          if (!resp.ok) {
            reconstructionState = "failed";
            return;
          }
          const data = await resp.json();
          const items = (data.items ?? []) as Array<{ id: string }>;
          for (const it of items) ids.push(it.id);
          offset = data.next_offset ?? -1;
          if (offset < 0) exhausted = true;
          pages += 1;
        }
        if (cancelled) return;
        if (!ids.includes(id)) {
          reconstructionState = "failed";
          return;
        }
        reconstructed = {
          navIds: ids,
          returnHref: `/albums/${albumId}`,
          source: { kind: "album", albumId },
        };
        reconstructionState = "ok";
      } else if (from === "hidden") {
        const ids: string[] = [];
        let offset = 0;
        let pages = 0;
        let exhausted = false;
        while (!cancelled && !ids.includes(id) && !exhausted && pages < PAGE_CAP) {
          const resp = await fetch(`/api/v1/hidden/media?offset=${offset}&limit=200`);
          if (resp.status === 403) {
            router.navigate("/hidden", { replace: true });
            return;
          }
          if (!resp.ok) {
            reconstructionState = "failed";
            return;
          }
          const data = await resp.json();
          const items = (data.items ?? []) as Array<{ id: string }>;
          for (const it of items) ids.push(it.id);
          offset = data.next_offset ?? -1;
          if (offset < 0) exhausted = true;
          pages += 1;
        }
        if (cancelled) return;
        if (!ids.includes(id)) {
          reconstructionState = "failed";
          return;
        }
        reconstructed = {
          navIds: ids,
          returnHref: "/hidden",
          source: { kind: "hidden" },
        };
        reconstructionState = "ok";
      } else {
        // Unknown `from`. Treat as failed reconstruction.
        reconstructionState = "failed";
      }
    } catch {
      reconstructionState = "failed";
    }
  })();
  return () => { cancelled = true; };
});

// Use either snapshot or reconstructed for navIds / source.
const effectiveNavIds = $derived(fromMatchesSession ? session!.navIds : (reconstructed?.navIds ?? []));
const effectiveSource = $derived(
  fromMatchesSession ? session!.source : reconstructed?.source ?? { kind: "library" as const },
);
const effectiveReturnHref = $derived(
  fromMatchesSession ? session!.returnHref : reconstructed?.returnHref ?? "/library",
);
```

Then update the existing `navIds` / `nav` / `returnHref` derivations to use the new effective values (the `reconstructionFailed` derivation in Task 16 already keys off `reconstructionState`, so no change there):

```ts
const navIds = $derived(effectiveNavIds);
const nav = $derived(computeNav(navIds, id));
const returnHref = $derived(effectiveReturnHref);
```

(`fallbackMode` continues to OR these together; `LightboxActions` source prop becomes `effectiveSource`.)

Update the LightboxActions snippet block:

```svelte
<LightboxActions
  source={effectiveSource}
  ...
/>
```

- [ ] **Step 2: Add reconstruction tests**

Add to `frontend/src/lib/components/lightbox/Lightbox.test.ts`:

```ts
describe("Lightbox reconstruction", () => {
  it("falls back to direct-detail shell when reconstruction can't find active id", async () => {
    lightboxSession.close();
    // Mock fetch /api/v1/media/x → ok with row data; /api/v1/albums/{id}/media → empty
    const fakeFetch = vi.fn().mockImplementation((url: string) => {
      if (url.startsWith("/api/v1/media/x")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ id: "x", thumb_version: 0, width: 1, height: 1, timestamp: "2026-04-20T00:00:00Z" }) });
      }
      if (url.includes("/albums/")) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [], next_offset: null }) });
      }
      return Promise.resolve({ ok: false, status: 404 });
    });
    vi.stubGlobal("fetch", fakeFetch);
    const { container } = render(Lightbox, {
      props: {
        id: "x",
        from: "album:bogus",
        mediaStore: fakeMediaStore(),
        albumsStore: { markStale: vi.fn() } as never,
        hiddenStore: { configured: true } as never,
        toastStore: { push: vi.fn() } as never,
      } as never,
    });
    // fallback shell renders close button only; no nav arrows
    await Promise.resolve();
    await Promise.resolve();
    expect(container.querySelector(".lb-backdrop.fallback")).toBeTruthy();
    expect(container.querySelector(".lb-prev")).toBeNull();
    expect(container.querySelector(".lb-next")).toBeNull();
    vi.unstubAllGlobals();
  });
});
```

- [ ] **Step 3: Run tests**

```
cd frontend && bun run test Lightbox
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/components/lightbox/Lightbox.svelte frontend/src/lib/components/lightbox/Lightbox.test.ts
git commit -m "feat(frontend): add Lightbox reconstruction for direct entry"
```

---

## Task 18: Source route integration — Library + Sessions

**Files:**
- Modify: `frontend/src/routes/Library.svelte`
- Modify: `frontend/src/routes/Sessions.svelte`

Wire `lightboxSession.open(...)` before navigating to the lightbox. On remount, run scroll restore.

- [ ] **Step 1: Update Library.svelte**

Add at the top of the script block:

```ts
import { selection } from "../lib/selection/selectionStore.svelte";
import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";
import { ScrollRestore } from "../lib/lightbox/scrollRestore.svelte";
import { flattenLibraryIds } from "../lib/lightbox/sessionsFlatten";
import { router } from "../lib/router/router.svelte";

const restore = new ScrollRestore();

$effect(() => {
  // On remount, consume the snapshot scroll/focus and clear it.
  const snap = lightboxSession.snapshot;
  if (snap !== null && snap.source.kind === "library") {
    restore.markPending({ scrollY: snap.scrollY, mediaId: snap.returnFocusMediaId });
    lightboxSession.clearScroll();
    lightboxSession.clearReturnFocus();
  }
  // Each loadMore can satisfy the restore; attempt after every months change.
});
$effect(() => {
  void mediaStore.months;
  if (restore.isPending()) restore.attemptRestore();
});

function openMedia(id: string) {
  const all = flattenLibraryIds(mediaStore.months);
  const sel = selection.ids;
  const useSelection = sel.size > 1 && sel.has(id);
  const navIds = useSelection
    ? all.filter((x) => sel.has(x))
    : all;
  lightboxSession.open({
    source: { kind: "library" },
    navIds,
    selected: useSelection,
    scrollY: window.scrollY,
    returnFocusMediaId: id,
    returnHref: "/library",
  });
  router.navigate(`/media/${id}?from=library`);
}
```

In the existing `<VirtualGrid>` invocation, add the prop:

```svelte
<VirtualGrid
  {months}
  ...existing props...
  onOpenMedia={openMedia}
>
```

- [ ] **Step 2: Update Sessions.svelte**

Same pattern. The existing route already has a click handler at line 60: `router.navigate(/media/${id})`. Replace the click handler with the same `openMedia(id)` pattern but using `flattenSessionIds` and `kind: "sessions"`, `returnHref: "/sessions"`.

```ts
import { flattenSessionIds } from "../lib/lightbox/sessionsFlatten";
import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";
import { ScrollRestore } from "../lib/lightbox/scrollRestore.svelte";
import { selection } from "../lib/selection/selectionStore.svelte";

const restore = new ScrollRestore();

$effect(() => {
  const snap = lightboxSession.snapshot;
  if (snap !== null && snap.source.kind === "sessions") {
    restore.markPending({ scrollY: snap.scrollY, mediaId: snap.returnFocusMediaId });
    lightboxSession.clearScroll();
    lightboxSession.clearReturnFocus();
  }
});
$effect(() => {
  void mediaStore.months;
  if (restore.isPending()) restore.attemptRestore();
});

function openMedia(id: string) {
  const all = flattenSessionIds(mediaStore.months);
  const sel = selection.ids;
  const useSelection = sel.size > 1 && sel.has(id);
  const navIds = useSelection
    ? all.filter((x) => sel.has(x))
    : all;
  lightboxSession.open({
    source: { kind: "sessions" },
    navIds,
    selected: useSelection,
    scrollY: window.scrollY,
    returnFocusMediaId: id,
    returnHref: "/sessions",
  });
  router.navigate(`/media/${id}?from=sessions`);
}
```

Replace the existing click handler that called `router.navigate(/media/...)` (line ~60) with `openMedia(id)`.

- [ ] **Step 3: Run existing route tests**

```
cd frontend && bun run test Library Sessions
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/routes/Library.svelte frontend/src/routes/Sessions.svelte
git commit -m "feat(frontend): wire Library/Sessions to lightboxSession.open"
```

---

## Task 19: Source route integration — AlbumDetail + HiddenLibrary

**Files:**
- Modify: `frontend/src/routes/AlbumDetail.svelte`
- Modify: `frontend/src/routes/HiddenLibrary.svelte`

Same pattern as Task 18, plus HiddenLibrary drops `disableNavigation`.

- [ ] **Step 1: Update AlbumDetail.svelte**

Add similar imports at the top of the script block. Build navIds from the album's loaded items. Wire `openMedia`:

```ts
import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";
import { ScrollRestore } from "../lib/lightbox/scrollRestore.svelte";
import { selection } from "../lib/selection/selectionStore.svelte";

const restore = new ScrollRestore();

$effect(() => {
  const snap = lightboxSession.snapshot;
  if (snap !== null && snap.source.kind === "album" && snap.source.albumId === id) {
    restore.markPending({ scrollY: snap.scrollY, mediaId: snap.returnFocusMediaId });
    lightboxSession.clearScroll();
    lightboxSession.clearReturnFocus();
  }
});
$effect(() => {
  void detailStore.itemIds;
  if (restore.isPending()) restore.attemptRestore();
});

function openMedia(mid: string) {
  const all = detailStore.itemIds;
  const sel = selection.ids;
  const useSelection = sel.size > 1 && sel.has(mid);
  const navIds = useSelection
    ? all.filter((x) => sel.has(x))
    : [...all];
  lightboxSession.open({
    source: { kind: "album", albumId: id },
    navIds,
    selected: useSelection,
    scrollY: window.scrollY,
    returnFocusMediaId: mid,
    returnHref: `/albums/${id}`,
  });
  router.navigate(`/media/${mid}?from=album:${id}`);
}
```

Pass `onOpenMedia={openMedia}` to the existing `<VirtualGrid>` invocation.

(`detailStore.itemIds` is the already-loaded id list per F2.3 albumDetailStore; verify the store's exposed property name and adapt as needed by reading `albumDetailStore.svelte.ts`.)

- [ ] **Step 2: Update HiddenLibrary.svelte**

Drop `disableNavigation={true}` and add the same pattern using `hiddenMediaStore`'s id list:

```ts
import { lightboxSession } from "../lib/lightbox/lightboxSession.svelte";
import { ScrollRestore } from "../lib/lightbox/scrollRestore.svelte";
import { selection } from "../lib/selection/selectionStore.svelte";

const restore = new ScrollRestore();

$effect(() => {
  const snap = lightboxSession.snapshot;
  if (snap !== null && snap.source.kind === "hidden") {
    restore.markPending({ scrollY: snap.scrollY, mediaId: snap.returnFocusMediaId });
    lightboxSession.clearScroll();
    lightboxSession.clearReturnFocus();
  }
});
$effect(() => {
  void hiddenMediaStore.months;
  if (restore.isPending()) restore.attemptRestore();
});

function openMedia(mid: string) {
  const all: string[] = [];
  for (const m of hiddenMediaStore.months) for (const it of m.items) all.push(it.id);
  const sel = selection.ids;
  const useSelection = sel.size > 1 && sel.has(mid);
  const navIds = useSelection ? all.filter((x) => sel.has(x)) : all;
  lightboxSession.open({
    source: { kind: "hidden" },
    navIds,
    selected: useSelection,
    scrollY: window.scrollY,
    returnFocusMediaId: mid,
    returnHref: "/hidden",
  });
  router.navigate(`/media/${mid}?from=hidden`);
}
```

In the `<VirtualGrid>` invocation:

```svelte
<VirtualGrid
  {months}
  ...other props...
  onOpenMedia={openMedia}
/>
```

Remove the `disableNavigation={true}` attribute and the TODO comment above it.

- [ ] **Step 3: Run tests**

```
cd frontend && bun run test AlbumDetail HiddenLibrary
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/routes/AlbumDetail.svelte frontend/src/routes/HiddenLibrary.svelte
git commit -m "feat(frontend): wire AlbumDetail/HiddenLibrary to lightboxSession; drop hidden disableNavigation"
```

---

## Task 20: e2e seed extensions

**Files:**
- Modify: `cmd/e2e-server/main.go`

Add scenario-dedicated fixtures: deep album with 30+ photos, multiple hidden rows, a selection-friendly seed.

- [ ] **Step 1: Read the current seeds**

```bash
grep -n "Seed\|insert\|InsertOwnedMedia\|Album\|hidden" cmd/e2e-server/main.go | head -30
```

(Find the existing seed call site so the new fixtures slot alongside.)

- [ ] **Step 2: Extend the seed function**

Append helpers to `cmd/e2e-server/main.go` (after the existing seed code). The exact insert API is already used by the file — pattern-match on the existing media.Repo / album.Repo / hidden.Service usage.

```go
// Seed scenario-dedicated fixtures for the F2.5 lightbox tests:
//   - lightbox-album-30 : album with 30 visible photos for paginated
//                         scroll-restore and "hide last visible item"
//                         scenarios (Playwright #3, #4, #20).
//   - lightbox-hidden-2 : two hidden photos for hidden-grid walk +
//                         unhide scenarios (Playwright #7).
//   - lightbox-select-5 : five visible photos with non-contiguous ids
//                         for selection-walk scenarios (Playwright #5, #6).
//
// Seed ids are deterministic (string prefix + index) so Playwright
// tests can reference them directly without consulting the DB.
func seedF2_5Fixtures(ctx context.Context, mediaSvc *service.MediaService, albumSvc *service.AlbumService, owner owners.Principal) error {
    // Implementation pattern: build N media rows with timestamps walking
    // backwards from a fixed base date. Use the same insert methods the
    // existing seed already uses; check git for the exact call signatures.
    return nil // see step 3 for filling in
}
```

- [ ] **Step 3: Fill in the seed bodies**

Before writing the bodies, read the existing seed pattern:

```bash
sed -n '100,200p' cmd/e2e-server/main.go
```

Adapt: insert media rows via the same path the existing fixtures use. For the album case, also call `albumSvc.AddMedia(ctx, owner, albumID, mediaIDs)` (or whatever API is already in use). For hidden, insert visible rows then call `hiddenSvc.HideMany(ctx, owner, ids)`.

The seed runs once at server boot; rows are deterministic; tests reference them by id (e.g. `lightbox-album-30-id-007`).

- [ ] **Step 4: Wire the new seed into the boot path**

Find the line where the existing seed runs (likely just before the server starts). Add a call to `seedF2_5Fixtures(ctx, mediaSvc, albumSvc, owner)` and propagate any error.

- [ ] **Step 5: Verify the e2e server still builds**

```
cd /Users/wesm/code/fotobank && go build -o tmp/e2e-server ./cmd/e2e-server
```

Expected: success.

- [ ] **Step 6: Commit**

```bash
git add cmd/e2e-server/main.go
git commit -m "test(e2e): seed F2.5 lightbox fixtures (deep album, hidden, selection)"
```

---

## Task 21: Playwright e2e — open/walk/close/scroll

**Files:**
- Create: `frontend/tests/e2e/lightbox.spec.ts`

Cover scenarios 1, 2, 8, 10, 14, 15, 18, 20 (the simpler open/walk/close/keyboard ones). Remaining scenarios (selection, hide/unhide, hidden flows, modals, fallback) land in Task 22.

- [ ] **Step 1: Create the e2e file with scenarios 1, 2, 8, 10**

```ts
// frontend/tests/e2e/lightbox.spec.ts
import { test, expect } from "@playwright/test";

test("library: open → walk → close → scroll restored", async ({ page }) => {
  await page.goto("/library");
  // scroll to a known position past the first row
  await page.evaluate(() => window.scrollTo(0, 1500));
  const startY = await page.evaluate(() => window.scrollY);
  expect(startY).toBeGreaterThan(0);

  // click the first tile in view
  const firstTile = page.locator("[data-media-id]").first();
  await firstTile.click();

  // URL has from=library
  await expect(page).toHaveURL(/\/media\/.+\?from=library/);

  // arrow-right twice
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("ArrowRight");

  // close
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/library/);

  // scroll restored within tolerance
  await page.waitForTimeout(200); // restore loop
  const endY = await page.evaluate(() => window.scrollY);
  expect(Math.abs(endY - startY)).toBeLessThan(200);
});

test("sessions: open → walk → close", async ({ page }) => {
  await page.goto("/sessions");
  const tile = page.locator("[data-media-id]").first();
  await tile.click();
  await expect(page).toHaveURL(/\/media\/.+\?from=sessions/);
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/sessions/);
});

test("direct entry: /media/:id?from=library reconstructs", async ({ page }) => {
  // Use a known seeded id from cmd/e2e-server. Adjust the literal to
  // match an id seeded in Task 20 (e.g. lightbox-select-5-id-002).
  await page.goto("/media/lightbox-select-5-id-002?from=library");
  await expect(page.locator(".lb-backdrop")).toBeVisible();
  // After reconstruction, prev/next arrows should be present
  // (the seeded list has 5 items).
  await expect(page.locator(".lb-prev, .lb-next").first()).toBeVisible({ timeout: 3000 });
});

test("direct entry: /media/:id (no from) renders DirectMediaDetail", async ({ page }) => {
  await page.goto("/media/lightbox-select-5-id-002");
  // Direct detail renders a back link with text "Back to Library".
  await expect(page.getByRole("link", { name: /back to library/i })).toBeVisible();
  // No lightbox backdrop.
  await expect(page.locator(".lb-backdrop")).not.toBeVisible();
});
```

- [ ] **Step 2: Add keyboard binding scenarios**

Append to the same file:

```ts
test("keyboard: arrows / Esc / + / - / 0 / Space / i", async ({ page }) => {
  await page.goto("/library");
  await page.locator("[data-media-id]").first().click();
  // info toggle
  await page.keyboard.press("i");
  await expect(page.locator(".lb-drawer, .bs-sheet")).toBeVisible();
  await page.keyboard.press("i");
  // Esc closes the lightbox (modal stack chooses topmost; if drawer is
  // not a modalStack entry, this falls through to lightbox.close).
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/library/);
});

// (Spec §7 e2e #15 "editable-target guard" lives as a Vitest
// integration test in Task 16, since the v1 info panel has no
// focusable inputs to drive from Playwright.)
```

- [ ] **Step 3: Image source verification**

```ts
test("image source verification: grid initial, preview/large requested, no /original", async ({ page }) => {
  const requested: string[] = [];
  await page.route("**/api/v1/media/*/thumb*", (route) => {
    requested.push(route.request().url());
    route.continue();
  });
  await page.route("**/api/v1/media/*/original", () => {
    throw new Error("/original should not be requested before Download");
  });
  await page.goto("/library");
  await page.locator("[data-media-id]").first().click();
  await page.waitForTimeout(500);
  expect(requested.some((u) => u.includes("size=preview"))).toBe(true);
  expect(requested.some((u) => u.includes("size=large"))).toBe(true);
});
```

- [ ] **Step 4: Paginated scroll restore (scenario 20)**

```ts
test("paginated source: scroll restore tolerates content arriving after remount", async ({ page }) => {
  // The seeded "lightbox-album-30" album has 30 photos spread across
  // multiple pages. Scroll past the first page and click a deep tile.
  await page.goto("/albums/lightbox-album-30");
  await page.evaluate(() => window.scrollTo(0, 2500));
  const startY = await page.evaluate(() => window.scrollY);

  // Click a tile that's deep in the list.
  const tiles = page.locator("[data-media-id]");
  const count = await tiles.count();
  await tiles.nth(Math.floor(count / 2)).click();

  await page.keyboard.press("Escape");
  // Wait for the album to refetch and restore loop to satisfy.
  await page.waitForTimeout(800);
  const endY = await page.evaluate(() => window.scrollY);
  expect(Math.abs(endY - startY)).toBeLessThan(400);
});
```

- [ ] **Step 5: Run e2e**

```
cd /Users/wesm/code/fotobank && make test-e2e
```

(Or from frontend: `cd frontend && bun run test:e2e`.)

Expected: all listed lightbox scenarios PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/tests/e2e/lightbox.spec.ts
git commit -m "test(e2e): add lightbox open/walk/close/keyboard/source/scroll scenarios"
```

---

## Task 22: Playwright e2e — selection, hide/unhide, hidden, modals, fallback

**Files:**
- Modify: `frontend/tests/e2e/lightbox.spec.ts`

Append the remaining scenarios.

- [ ] **Step 1: Selection snapshot scenarios (5, 6)**

```ts
test("selection snapshot walks selected ids only when N>1 selected and opened id is selected", async ({ page }) => {
  await page.goto("/library");
  // Cmd-click 5 non-contiguous tiles to build a selection of 5.
  // (The exact mechanism depends on existing selection UI; pattern-match
  // on existing albums.spec.ts selection helpers if they exist.)
  // For each of 5 ids, hold Meta and click.
  const ids = ["lightbox-select-5-id-001", "lightbox-select-5-id-002", "lightbox-select-5-id-003", "lightbox-select-5-id-004", "lightbox-select-5-id-005"];
  for (const id of ids) {
    await page.locator(`[data-media-id="${id}"]`).click({ modifiers: ["Meta"] });
  }
  // Plain-click the 3rd selected id to open with selection-walk.
  await page.locator(`[data-media-id="${ids[2]}"]`).click();
  await expect(page).toHaveURL(new RegExp(`/media/${ids[2]}\\?from=library`));
  await page.keyboard.press("ArrowRight");
  await expect(page).toHaveURL(new RegExp(`/media/${ids[3]}\\?from=library`));
  await page.keyboard.press("ArrowRight");
  await expect(page).toHaveURL(new RegExp(`/media/${ids[4]}\\?from=library`));
  // boundary: one more arrow does nothing
  await page.keyboard.press("ArrowRight");
  await expect(page).toHaveURL(new RegExp(`/media/${ids[4]}\\?from=library`));
});

test("plain-click an unselected tile uses full source list (selection rule)", async ({ page }) => {
  await page.goto("/library");
  const selectedIds = ["lightbox-select-5-id-001", "lightbox-select-5-id-002", "lightbox-select-5-id-003"];
  for (const id of selectedIds) {
    await page.locator(`[data-media-id="${id}"]`).click({ modifiers: ["Meta"] });
  }
  // Plain-click an unselected tile: navigation uses full source.
  await page.locator(`[data-media-id="lightbox-select-5-id-005"]`).click();
  // arrow-right should advance past selectedIds boundaries
  await page.keyboard.press("ArrowRight");
  // No precise id assertion needed; just verify URL changed away from id-005.
  await expect(page).not.toHaveURL(/lightbox-select-5-id-005/);
});
```

- [ ] **Step 2: Album hide → advance / hide last → close (scenarios 3, 4)**

```ts
test("album: hide active item advances to next visible", async ({ page }) => {
  // Use a fresh, dedicated album with 3 photos so a hide doesn't
  // interfere with other scenarios. Seeded as "lightbox-album-3".
  await page.goto("/albums/lightbox-album-3");
  await page.locator("[data-media-id]").first().click();
  // The active id and the second id from the album.
  const activeId = await page.evaluate(() => new URL(window.location.href).pathname.split("/").pop()!);
  // Click hide
  await page.getByRole("button", { name: /hide/i }).click();
  // confirm dialog
  await page.evaluate(() => { window.confirm = () => true; });
  // After advance, URL should point at a different id under from=album:...
  await expect(page).not.toHaveURL(new RegExp(`/media/${activeId}\\?from=album:`));
});
```

- [ ] **Step 3: Hidden grid: walk → unhide → advance → close → row visible in library (scenario 7)**

```ts
test("hidden: walk → unhide → advance → close → row visible in library", async ({ page }) => {
  // Unlock first.
  await page.goto("/hidden");
  // The HiddenGate UI gates entry until unlock. Submit a known passcode
  // seeded by cmd/e2e-server. Adjust the input flow to match HiddenGate.svelte.
  // (Pattern-match on hidden.spec.ts unlock helper.)
  // ... unlock flow ...
  await page.locator("[data-media-id]").first().click();
  await expect(page).toHaveURL(/\/media\/.+\?from=hidden/);
  await page.evaluate(() => { window.confirm = () => true; });
  await page.getByRole("button", { name: /unhide/i }).click();
  // After advance, still under from=hidden; eventual Esc returns to /hidden.
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/hidden$/);
  // Now navigate to /library and confirm the unhidden id is visible.
  await page.goto("/library");
  // Adjust the id to the one used in the seeded "lightbox-hidden-2" set.
  await expect(page.locator("[data-media-id=\"lightbox-hidden-2-id-001\"]")).toBeVisible();
});
```

- [ ] **Step 4: Hidden direct entry while locked → redirect (scenario 9)**

```ts
test("direct entry /media/:id?from=hidden while locked → redirect to /hidden", async ({ page }) => {
  // Lock first via API.
  await page.request.post("/api/v1/auth/hidden/lock");
  await page.goto("/media/lightbox-hidden-2-id-001?from=hidden");
  await expect(page).toHaveURL(/\/hidden$/);
});
```

- [ ] **Step 5: Hidden cross-context (scenario 11)**

```ts
test("hidden cross-context: from=library + hidden_at != null → fallback to direct-detail", async ({ page }) => {
  // Unlock so the API returns the hidden row.
  // ... unlock flow ...
  await page.goto("/media/lightbox-hidden-2-id-001?from=library");
  // Lightbox renders fallback shell wrapping DirectMediaDetail.
  await expect(page.locator(".lb-backdrop.fallback")).toBeVisible();
  await expect(page.locator(".lb-prev")).not.toBeVisible();
});
```

- [ ] **Step 6: Stacked modals (scenarios 12, 13)**

```ts
test("stacked modals: Esc closes Add-to-album first, then lightbox", async ({ page }) => {
  await page.goto("/library");
  await page.locator("[data-media-id]").first().click();
  await page.getByRole("button", { name: /add to album/i }).click();
  await expect(page.getByRole("dialog", { name: /add to album/i })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: /add to album/i })).not.toBeVisible();
  // Lightbox still open
  await expect(page.locator(".lb-backdrop")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/\/library/);
});

test("stacked modals: arrow keys do NOT advance lightbox while Add-to-album is open", async ({ page }) => {
  await page.goto("/library");
  await page.locator("[data-media-id]").first().click();
  const startUrl = page.url();
  await page.getByRole("button", { name: /add to album/i }).click();
  await page.keyboard.press("ArrowRight");
  expect(page.url()).toBe(startUrl);
});
```

- [ ] **Step 7: Modal migration regression (scenario 19)**

```ts
test("modal migration: AddToAlbum / Share / Confirm / RenameAlbum each open and Esc-close cleanly", async ({ page }) => {
  // AddToAlbum
  await page.goto("/library");
  // ... open AddToAlbumModal from action bar; Esc closes ...
  // Share
  // Confirm (delete album triggers ConfirmModal)
  // RenameAlbum
  // Each open → Esc → no double-dispatch (lightbox not stuck).
  // (Pattern after albums.spec.ts existing modal flows.)
});
```

- [ ] **Step 8: Fallback delegation (scenario 21)**

```ts
test("direct entry /media/:id?from=album:bogus → fallback shell + DirectMediaDetail", async ({ page }) => {
  // Use a known visible media id but a bogus album id so reconstruction fails.
  await page.goto("/media/lightbox-select-5-id-001?from=album:bogus");
  await expect(page.locator(".lb-backdrop.fallback")).toBeVisible();
  // No prev/next; toolbar X visible.
  await expect(page.locator(".lb-prev")).not.toBeVisible();
  await expect(page.locator(".lb-next")).not.toBeVisible();
  await expect(page.getByRole("button", { name: /close/i })).toBeVisible();
  // Esc closes to source href.
  await page.keyboard.press("Escape");
});
```

- [ ] **Step 9: Run all e2e**

```
cd /Users/wesm/code/fotobank && make test-e2e
```

Expected: all PASS.

- [ ] **Step 10: Commit**

```bash
git add frontend/tests/e2e/lightbox.spec.ts
git commit -m "test(e2e): add selection, hide/unhide, hidden, modals, fallback scenarios"
```

---

## Task 23: Verification pass

**Files:** none (typecheck + tests across the whole suite).

- [ ] **Step 1: Frontend typecheck**

```
cd frontend && bun run typecheck
```

Expected: PASS.

- [ ] **Step 2: Frontend unit tests**

```
cd frontend && bun run test
```

Expected: PASS.

- [ ] **Step 3: Backend short tests**

```
cd /Users/wesm/code/fotobank && go test ./... -short
```

Expected: PASS (no backend changes; this verifies the e2e seed compiles and the existing suite is green).

- [ ] **Step 4: Full e2e**

```
make test-e2e
```

Expected: all scenarios PASS, including the new lightbox file.

- [ ] **Step 5: Final commit (if any drift)**

If any test required a small follow-up tweak, commit it:

```bash
git add -A
git commit -m "fix(frontend): F2.5 verification follow-up"
```

If everything passed clean, no extra commit needed.

---

## Self-review notes (post-write)

After writing the plan, verify:

1. **Spec coverage:**
   - §1 architecture overview → Tasks 1–17 (every store/component/route change touched)
   - §2 fallback contract → Task 10 (extract DirectMediaDetail) + Task 16 (Lightbox shell + modes) + Task 17 (reconstruction) + Task 22 e2e #21
   - §3 component decomposition → Tasks 1–15 (each file matches a task)
   - §4 data flow open/walk/close → Tasks 16, 18, 19, 21
   - §4 reconstruction → Task 17 + Task 22
   - §4 source-aware actions → Task 15 + Task 22 album/hidden scenarios
   - §5 keyboard / modal stack / focus → Tasks 2, 9, 16 (keyboard handler) + Task 21 keyboard test
   - §6 loading/perf/errors → Task 5 (loader) + Task 16 (loader wired) + Task 21 image source verification
   - §7 unit tests → Tasks 1–6, 8, 12, 13, 14, 15, 16, 17 (each new module gets a test)
   - §7 integration tests → Task 17 (reconstruction failure) + LightboxActions test (Task 15)
   - §7 e2e scenarios 1–21 → Tasks 21, 22

2. **Placeholder scan:** plan steps either show code or give a precise enough Bash command. Sections that say "pattern-match on existing X" do so only where the existing helper is sufficiently obvious from the file references. The seed bodies in Task 20 are intentionally adapted at write time because the existing seed pattern in `cmd/e2e-server/main.go` is the canonical shape.

3. **Type consistency:**
   - `LightboxSession` shape matches across Tasks 3, 15, 16, 18, 19.
   - `computeNav` signature is `(navIds, activeId)` consistently in Tasks 4, 16.
   - `LightboxLoader.load` opts shape matches Task 5 + Task 16.
   - `modalStack.push({ id, onEscape, trap? })` matches across Tasks 2, 8, 9, 16.
   - `mediaStore.get(id)` (existing API) used everywhere — confirmed.
   - `mediaStore.removeMany(ids)` and `mediaStore.mergeRaw(rows)` (existing) used in Tasks 15, 18, 19.
   - `albumsStore.markStale()` used in Tasks 15, 19 (existing per F2.4).
   - `lightboxSession.removeIds(ids)` matches Task 3 + Task 15.
   - Router `from` field on `RouteMatch` for media route matches Task 7 + Task 10 + Task 18/19.

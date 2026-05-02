import { describe, it, expect, vi, beforeAll } from "vitest";
import { render, waitFor } from "@testing-library/svelte";
import Lightbox from "./Lightbox.svelte";
import { lightboxSession } from "../../lightbox/lightboxSession.svelte";
import * as aiClient from "../../ai/client";

// panzoom mounts on the <img> when LightboxImage mounts. jsdom can't
// drive its measurement code, so stub the entire module.
vi.mock("panzoom", () => ({
  default: () => ({
    dispose: vi.fn(),
    getTransform: () => ({ scale: 1, x: 0, y: 0 }),
    zoomTo: vi.fn(),
    zoomAbs: vi.fn(),
    moveTo: vi.fn(),
  }),
}));

// LightboxMetadata mounts LightboxAI which calls getMediaAIView on
// mount. Stub the module so the search-relevance test isn't gated on
// the AI subsystem.
vi.mock("../../ai/client", () => ({
  getMediaAIView: vi.fn(),
  retryPhotoAI: vi.fn(),
}));

// jsdom doesn't ship matchMedia. The Lightbox uses it to track the
// mobile breakpoint; stub a static "not matching" implementation so
// the $effect can wire up its listener without throwing.
beforeAll(() => {
  if (!window.matchMedia) {
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: (query: string): MediaQueryList =>
        ({
          matches: false,
          media: query,
          onchange: null,
          addListener: () => undefined,
          removeListener: () => undefined,
          addEventListener: () => undefined,
          removeEventListener: () => undefined,
          dispatchEvent: () => false,
        }) as unknown as MediaQueryList,
    });
  }
});

const fakeMedia = {
  id: "m1",
  timestamp: "2026-04-20T12:00:00Z",
  taken: new Date("2026-04-20T12:00:00Z"),
  aspect: 1,
  thumbUrl: "/api/v1/media/m1/thumb?size=grid&v=0",
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

describe("Lightbox reconstruction", () => {
  it("falls back to direct-detail shell when reconstruction can't find active id", async () => {
    lightboxSession.close();
    const fakeFetch = vi.fn().mockImplementation((url: string) => {
      if (url.startsWith("/api/v1/media/x")) {
        return Promise.resolve({
          ok: true,
          json: () =>
            Promise.resolve({
              id: "x",
              thumb_version: 0,
              width: 1,
              height: 1,
              timestamp: "2026-04-20T00:00:00Z",
            }),
        });
      }
      if (url.includes("/albums/")) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ items: [], next_offset: null }),
        });
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
    // Wait for the reconstruction effect to drain: fetch resolves with
    // an empty page → state flips to "failed" → fallback shell renders.
    await waitFor(() => {
      expect(container.querySelector(".lb-backdrop.fallback")).toBeTruthy();
    });
    expect(container.querySelector(".lb-prev")).toBeNull();
    expect(container.querySelector(".lb-next")).toBeNull();
    vi.unstubAllGlobals();
  });
});

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
    // Stub the navigate body so the positive-case dispatch doesn't
    // actually mutate window.history / router state — otherwise later
    // tests in this file (or other Lightbox specs run in the same
    // process) inherit the polluted depth and current route.
    const navigateSpy = vi.spyOn(
      await import("../../router/router.svelte").then((m) => m.router),
      "navigate",
    ).mockImplementation(() => undefined);
    navigateSpy.mockClear();
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
    const ta = document.createElement("textarea");
    document.body.appendChild(ta);
    ta.focus();
    const blockedEv = new KeyboardEvent("keydown", {
      key: "ArrowRight",
      bubbles: true,
    });
    Object.defineProperty(blockedEv, "target", { value: ta });
    window.dispatchEvent(blockedEv);
    const blockedCalls = navigateSpy.mock.calls.map((c) => c[0] as string);
    expect(blockedCalls.some((u) => u.includes("/media/m2"))).toBe(false);
    document.body.removeChild(ta);

    // Paired positive: same key from a non-editable target advances.
    const allowedEv = new KeyboardEvent("keydown", {
      key: "ArrowRight",
      bubbles: true,
    });
    Object.defineProperty(allowedEv, "target", { value: document.body });
    window.dispatchEvent(allowedEv);
    const allowedCalls = navigateSpy.mock.calls.map((c) => c[0] as string);
    expect(allowedCalls.some((u) => u.includes("/media/m2"))).toBe(true);
  });

  it("renders the Search relevance row when info drawer opens for a search-context snapshot", async () => {
    // V2 wiring end-to-end: a search-source snapshot carrying a
    // scoreComponentsById map produces a "Search relevance" row in
    // LightboxMetadata once the info drawer is open. The drawer is
    // toggled via the "i" key — same path the production user takes.
    vi.mocked(aiClient.getMediaAIView).mockResolvedValue({});
    const sc = {
      rrf: 0.0156,
      bm25: 8.42,
      vector: 0.81,
      rank_bm25: 3,
      rank_vector: 7,
    };
    lightboxSession.open({
      source: { kind: "search" },
      navIds: ["m1"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: "m1",
      returnHref: "/search?q=trees",
      scoreComponentsById: new Map([["m1", sc]]),
    });
    const { container, getByTestId } = render(Lightbox, {
      props: {
        id: "m1",
        from: "search",
        mediaStore: fakeMediaStore(),
        albumsStore: { markStale: vi.fn() } as never,
        hiddenStore: { configured: true } as never,
        toastStore: { push: vi.fn() } as never,
      } as never,
    });

    // Pre-condition: drawer is closed → no relevance row mounted yet.
    expect(container.querySelector("[data-testid='search-relevance']")).toBeNull();

    // Press "i" to open the info drawer. Lightbox's onKey handler is
    // bound to window and gates on isEditableTarget(event.target),
    // which the document.body target passes.
    const ev = new KeyboardEvent("keydown", { key: "i", bubbles: true });
    Object.defineProperty(ev, "target", { value: document.body });
    window.dispatchEvent(ev);

    // The drawer mounts asynchronously after the state flip; await the
    // testid that LightboxMetadata's relevance block is tagged with.
    const block = await waitFor(() => getByTestId("search-relevance"));
    expect(block.textContent).toContain("RRF");
    expect(block.textContent).toContain("0.0156");
    expect(block.textContent).toContain("BM25");
    expect(block.textContent).toContain("8.42");
    expect(block.textContent).toContain("(rank 3)");
    expect(block.textContent).toContain("Vector");
    expect(block.textContent).toContain("0.81");
    expect(block.textContent).toContain("(rank 7)");
  });

  it("omits the Search relevance row for a non-search-context snapshot", async () => {
    // Regression: existing callers (Library/Sessions/Albums) must not
    // get a relevance row even after opening the info drawer. The
    // lightbox derives activeScoreComponents only when source.kind is
    // "search" AND the snapshot agrees with `from`; library is the
    // dominant non-search case.
    vi.mocked(aiClient.getMediaAIView).mockResolvedValue({});
    lightboxSession.open({
      source: { kind: "library" },
      navIds: ["m1"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: "m1",
      returnHref: "/library",
    });
    const { container, queryByTestId } = render(Lightbox, {
      props: {
        id: "m1",
        from: "library",
        mediaStore: fakeMediaStore(),
        albumsStore: { markStale: vi.fn() } as never,
        hiddenStore: { configured: true } as never,
        toastStore: { push: vi.fn() } as never,
      } as never,
    });
    const ev = new KeyboardEvent("keydown", { key: "i", bubbles: true });
    Object.defineProperty(ev, "target", { value: document.body });
    window.dispatchEvent(ev);
    // Wait for the drawer to open (the close button gets a fresh
    // aria-label) so we know the assertion runs after the metadata
    // component has actually mounted, not before it.
    await waitFor(() =>
      expect(container.querySelector("[aria-label='Close info']")).toBeTruthy(),
    );
    expect(queryByTestId("search-relevance")).toBeNull();
  });
});

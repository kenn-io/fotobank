import { describe, it, expect, vi, beforeAll } from "vitest";
import { render, waitFor } from "@testing-library/svelte";
import Lightbox from "./Lightbox.svelte";
import { lightboxSession } from "../../lightbox/lightboxSession.svelte";

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
});

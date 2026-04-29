import { describe, it, expect, vi, beforeAll } from "vitest";
import { render } from "@testing-library/svelte";
import HiddenLibrary from "./HiddenLibrary.svelte";
import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
import type { HiddenMediaStore } from "../lib/hidden/hiddenMediaStore.svelte";

// VirtualGrid uses ResizeObserver + IntersectionObserver in $effect blocks;
// jsdom doesn't ship them so we stub no-ops, same pattern as VirtualGrid.flat.test.ts.
beforeAll(() => {
  class NoopResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  class NoopIntersectionObserver {
    root = null;
    rootMargin = "";
    thresholds: number[] = [];
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords(): IntersectionObserverEntry[] { return []; }
  }
  vi.stubGlobal("ResizeObserver", NoopResizeObserver);
  vi.stubGlobal("IntersectionObserver", NoopIntersectionObserver);
});

function makeStore(overrides: Partial<HiddenStore> = {}): HiddenStore {
  return {
    configured: false,
    unlocked: false,
    expiresAt: null,
    lockedOutUntil: null,
    error: null,
    refresh: vi.fn(),
    unlock: vi.fn(),
    lock: vi.fn(),
    hide: vi.fn(),
    unhide: vi.fn(),
    ...overrides,
  } as unknown as HiddenStore;
}

function makeMediaStore(overrides: Partial<HiddenMediaStore> = {}): HiddenMediaStore {
  return {
    months: [],
    loading: false,
    exhausted: false,
    loadInitial: vi.fn().mockResolvedValue(undefined),
    loadMore: vi.fn().mockResolvedValue(undefined),
    removeMany: vi.fn(),
    ...overrides,
  } as unknown as HiddenMediaStore;
}

describe("HiddenLibrary", () => {
  it("shows CTA when not configured", () => {
    const store = makeStore({ configured: false });
    const { container } = render(HiddenLibrary, { props: { hiddenStore: store } });
    expect(container.textContent).toContain("fotobank hidden setup");
  });

  it("shows passcode gate when configured and locked", () => {
    const store = makeStore({ configured: true, unlocked: false });
    const { container } = render(HiddenLibrary, { props: { hiddenStore: store } });
    const input = container.querySelector("input[type='password']");
    expect(input).not.toBeNull();
  });

  it("shows empty state grid area when configured and unlocked with no items", () => {
    const store = makeStore({ configured: true, unlocked: true });
    const mediaStore = makeMediaStore();
    const { container } = render(HiddenLibrary, {
      props: { hiddenStore: store, hiddenMediaStore: mediaStore },
    });
    // No passcode form when unlocked
    const input = container.querySelector("input[type='password']");
    expect(input).toBeNull();
    // No CTA text when configured
    expect(container.textContent).not.toContain("fotobank hidden setup");
  });

  it("calls loadInitial on the media store when configured and unlocked", async () => {
    const store = makeStore({ configured: true, unlocked: true });
    const mediaStore = makeMediaStore();
    render(HiddenLibrary, {
      props: { hiddenStore: store, hiddenMediaStore: mediaStore },
    });
    // $effect fires synchronously in test environment on first render
    expect(mediaStore.loadInitial).toHaveBeenCalled();
  });

  it("does not call loadInitial when not configured", () => {
    const store = makeStore({ configured: false, unlocked: false });
    const mediaStore = makeMediaStore();
    render(HiddenLibrary, {
      props: { hiddenStore: store, hiddenMediaStore: mediaStore },
    });
    expect(mediaStore.loadInitial).not.toHaveBeenCalled();
  });
});

import { describe, it, expect, vi, beforeAll, type Mock } from "vitest";
import { render } from "@testing-library/svelte";
import AlbumDetail from "./AlbumDetail.svelte";
import { MediaStore } from "../lib/media/mediaStore.svelte";
import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
import type { ToastStore } from "../lib/toasts/toastStore.svelte";
import { AppConfigStore } from "../lib/app/appConfig.svelte";
import type { Client } from "../lib/api/client";

function defaultAppConfig(): AppConfigStore {
  const c = {
    GET: async () => ({ data: undefined, error: { status: 0 } }),
  } as unknown as Pick<Client, "GET">;
  return new AppConfigStore(c);
}

// AlbumDetail creates AlbumDetailStore with the real `api` singleton.
// We mock the module so tests can control what GET returns.
vi.mock("../lib/api/client", () => {
  let _get = vi.fn().mockResolvedValue({ data: null });
  const client = {
    GET: (...args: unknown[]) => _get(...args),
    POST: vi.fn(),
    PATCH: vi.fn(),
    DELETE: vi.fn(),
  };
  return {
    api: client,
    createApiClient: () => client,
    _setGet: (fn: typeof _get) => { _get = fn; },
  };
});

// VirtualGrid uses ResizeObserver + IntersectionObserver in $effect blocks.
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

function makeAlbumsStore(): AlbumsStore {
  return {
    albums: [],
    loading: false,
    exhausted: true,
    loadError: false,
    loadInitial: vi.fn().mockResolvedValue(undefined),
    markStale: vi.fn(),
    refreshIfStale: vi.fn().mockResolvedValue(undefined),
    loadMore: vi.fn(),
    retry: vi.fn(),
    create: vi.fn(),
    rename: vi.fn(),
    delete: vi.fn(),
    applyRename: vi.fn(),
    dropLocal: vi.fn(),
    byId: () => undefined,
  } as unknown as AlbumsStore;
}

function makeHiddenStore(overrides: Partial<HiddenStore> = {}): HiddenStore {
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

function makeToastStore(): ToastStore {
  return {
    items: [],
    push: vi.fn(),
    dismiss: vi.fn(),
    clear: vi.fn(),
  } as unknown as ToastStore;
}

// Load the module mock accessor so we can configure GET responses per test.
async function getSetGet(): Promise<(fn: Mock) => void> {
  const m = await import("../lib/api/client") as unknown as { _setGet: (fn: Mock) => void };
  return m._setGet;
}

function makeGetSequence(...responses: unknown[]) {
  let i = 0;
  return vi.fn(async () => responses[i++] ?? { data: null });
}

describe("AlbumDetail header hidden chip", () => {
  it("does not show hidden chip when hidden_count is 0", async () => {
    const setGet = await getSetGet();
    setGet(makeGetSequence(
      { data: { id: "a1", name: "Italy", item_count: 95, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [], next_offset: null } },
    ));
    const ms = new MediaStore({ GET: vi.fn() } as never);
    const { container } = render(AlbumDetail, {
      props: {
        id: "a1",
        mediaStore: ms,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore(),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(container.querySelector(".hidden-chip")).toBeNull();
  });

  it("shows hidden chip when hidden_count > 0", async () => {
    const setGet = await getSetGet();
    setGet(makeGetSequence(
      { data: { id: "a1", name: "Italy", item_count: 95, hidden_count: 5, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [], next_offset: null } },
    ));
    const ms = new MediaStore({ GET: vi.fn() } as never);
    const { container } = render(AlbumDetail, {
      props: {
        id: "a1",
        mediaStore: ms,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore(),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    await new Promise((r) => setTimeout(r, 0));
    const chip = container.querySelector(".hidden-chip");
    expect(chip).not.toBeNull();
    expect(chip?.textContent).toContain("5 hidden");
  });
});

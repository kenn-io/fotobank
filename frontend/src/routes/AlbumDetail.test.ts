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

listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
getAlbum(id?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
listAlbumMedia(id?: any, params?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}/media", { params: { path: { id }, query: params }, ...options }); },
hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); }
} as unknown as Client;
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

listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
createAlbum(albumNameRequest?: any, options?: any) { return (this as any).POST("/api/v1/albums", { body: albumNameRequest, ...options }); },
deleteAlbum(id?: any, options?: any) { return (this as any).DELETE("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
getAlbum(id?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
renameAlbum(id?: any, albumNameRequest?: any, options?: any) { return (this as any).PATCH("/api/v1/albums/{id}", { params: { path: { id } }, body: albumNameRequest, ...options }); },
listAlbumMedia(id?: any, params?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}/media", { params: { path: { id }, query: params }, ...options }); },
addMediaToAlbum(id?: any, addAlbumMediaRequest?: any, options?: any) { return (this as any).POST("/api/v1/albums/{id}/media", { params: { path: { id } }, body: addAlbumMediaRequest, ...options }); },
removeAlbumMedia(id?: any, mediaId?: any, options?: any) { return (this as any).DELETE("/api/v1/albums/{id}/media/{media_id}", { params: { path: { id, media_id: mediaId } }, body: mediaId, ...options }); },
hiddenLock(options?: any) { return (this as any).POST("/api/v1/auth/hidden/lock", { ...options }); },
hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
hiddenUnlock(hiddenPasscodeRequest?: any, options?: any) { return (this as any).POST("/api/v1/auth/hidden/unlock", { body: hiddenPasscodeRequest, ...options }); },
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); },
hideMediaBulk(hiddenMediaBulkInputBody?: any, options?: any) { return (this as any).POST("/api/v1/media/hidden:bulk", { body: hiddenMediaBulkInputBody, ...options }); },
unhideMediaBulk(hiddenMediaBulkInputBody?: any, options?: any) { return (this as any).POST("/api/v1/media/unhide:bulk", { body: hiddenMediaBulkInputBody, ...options }); },
sharesCreate(createShareRequest?: any, options?: any) { return (this as any).POST("/api/v1/shares", { body: createShareRequest, ...options }); }
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

async function getApiMock(): Promise<{
  api: { POST: Mock; PATCH: Mock; DELETE: Mock };
}> {
  return (await import("../lib/api/client")) as unknown as {
    api: { POST: Mock; PATCH: Mock; DELETE: Mock };
  };
}

describe("AlbumDetail header hidden chip", () => {
  it("does not show hidden chip when hidden_count is 0", async () => {
    const setGet = await getSetGet();
    setGet(makeGetSequence(
      { data: { id: "a1", name: "Italy", item_count: 95, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [], next_offset: null } },
    ));
    const ms = new MediaStore({ GET: vi.fn() ,
listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
getAlbum(id?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
listAlbumMedia(id?: any, params?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}/media", { params: { path: { id }, query: params }, ...options }); },
hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); }
} as never);
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
    const ms = new MediaStore({ GET: vi.fn() ,
listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
getAlbum(id?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
listAlbumMedia(id?: any, params?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}/media", { params: { path: { id }, query: params }, ...options }); },
hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); }
} as never);
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

describe("AlbumDetail sharing gate", () => {
  it("does not render the Share album button when sharing UI is disabled", async () => {
    const setGet = await getSetGet();
    setGet(makeGetSequence(
      { data: { id: "a1", name: "Italy", item_count: 5, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [], next_offset: null } },
    ));
    const ms = new MediaStore({ GET: vi.fn() ,
listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
getAlbum(id?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
listAlbumMedia(id?: any, params?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}/media", { params: { path: { id }, query: params }, ...options }); },
hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); }
} as never);
    const { container, findByText } = render(AlbumDetail, {
      props: {
        id: "a1",
        mediaStore: ms,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore(),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    await findByText("Italy");
    const buttons = Array.from(container.querySelectorAll("button"));
    const shareBtn = buttons.find((b) => /share album/i.test(b.textContent ?? ""));
    expect(shareBtn).toBeUndefined();
  });

  it("renders the Share album button when sharing UI is enabled", async () => {
    const setGet = await getSetGet();
    setGet(makeGetSequence(
      { data: { id: "a1", name: "Italy", item_count: 5, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [], next_offset: null } },
    ));
    const enabledClient = {
      GET: async () => ({
        data: {
          principal: { hub: "h", user_id: "u", handle: "" },
          scopes: [],
          features: { sharing_enabled: true },
        },
      }),

listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
getAlbum(id?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
listAlbumMedia(id?: any, params?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}/media", { params: { path: { id }, query: params }, ...options }); },
hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); }
} as unknown as Client;
    const cfg = new AppConfigStore(enabledClient);
    await cfg.load();

    const ms = new MediaStore({ GET: vi.fn() ,
listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
getAlbum(id?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
listAlbumMedia(id?: any, params?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}/media", { params: { path: { id }, query: params }, ...options }); },
hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); }
} as never);
    const { container, findByText } = render(AlbumDetail, {
      props: {
        id: "a1",
        mediaStore: ms,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore(),
        toastStore: makeToastStore(),
        appConfig: cfg,
      },
    });
    await findByText("Italy");
    const buttons = Array.from(container.querySelectorAll("button"));
    const shareBtn = buttons.find((b) => /share album/i.test(b.textContent ?? ""));
    expect(shareBtn).not.toBeUndefined();
  });
});

describe("AlbumDetail delete-blocked toast copy", () => {
  it("references the CLI command, not the in-app /shares page", async () => {
    const setGet = await getSetGet();
    setGet(makeGetSequence(
      { data: { id: "album-123", name: "Italy", item_count: 5, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [], next_offset: null } },
    ));
    const mod = await getApiMock();
    mod.api.DELETE.mockResolvedValue({ error: { status: 409, message: "album has active shares" } });

    const ms = new MediaStore({ GET: vi.fn() ,
listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
getAlbum(id?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
listAlbumMedia(id?: any, params?: any, options?: any) { return (this as any).GET("/api/v1/albums/{id}/media", { params: { path: { id }, query: params }, ...options }); },
hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); }
} as never);
    const { container, findByText } = render(AlbumDetail, {
      props: {
        id: "album-123",
        mediaStore: ms,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore(),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    await findByText("Italy");

    // Click the header Delete button to open ConfirmModal.
    const headerButtons = Array.from(container.querySelectorAll("button"));
    const headerDelete = headerButtons.find((b) => (b.textContent ?? "").trim() === "Delete");
    expect(headerDelete).not.toBeUndefined();
    headerDelete!.click();
    // Wait for ConfirmModal to mount.
    await new Promise((r) => setTimeout(r, 0));
    // Find the modal's Confirm button (also labeled "Delete"). Two
    // buttons now share the label — pick the one inside the modal
    // dialog so we don't re-trigger the header.
    const modalConfirm = container.querySelector('[role="dialog"] button.danger');
    expect(modalConfirm).not.toBeNull();
    (modalConfirm as HTMLButtonElement).click();
    // delete() awaits, then catch sets deleteConflictAlbumId. Wait
    // for the toast to render.
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));

    const toast = await findByText(/active CLI shares/i);
    expect(toast).not.toBeNull();
    const toastEl = toast.closest(".conflict-toast");
    expect(toastEl).not.toBeNull();
    const toastText = toastEl!.textContent ?? "";
    expect(toastText).toContain("fotobank shares list --album");
    expect(toastText).toContain("album-123");
    // The old copy referenced /shares — it must be gone.
    expect(toastText).not.toContain("View shares");
    expect(toastText).not.toContain("Revoke them in Shares first.");
  });
});

import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import DirectMediaDetail from "./DirectMediaDetail.svelte";
import { MediaStore } from "../media/mediaStore.svelte";
import type { AlbumsStore } from "../albums/albumsStore.svelte";
import type { HiddenStore } from "../hidden/hiddenStore.svelte";
import type { ToastStore } from "../toasts/toastStore.svelte";
import { AppConfigStore } from "../app/appConfig.svelte";
import type { Client } from "../api/client";

function defaultAppConfig(): AppConfigStore {
  const client = {
    GET: async () => ({ data: undefined, error: { status: 0 } }),
  } as unknown as Pick<Client, "GET">;
  return new AppConfigStore(client);
}

function storeWith(raw: Record<string, unknown>): MediaStore {
  const s = new MediaStore({ GET: vi.fn() } as never);
  s.mergeRaw([raw]);
  return s;
}

// Minimal AlbumsStore stub: DirectMediaDetail only forwards the prop into
// AddToAlbumModal, which isn't mounted unless the user opens the modal.
// Tests in this file don't open it, so a frozen empty-list stub is enough.
function makeAlbumsStore(): AlbumsStore {
  return {
    albums: [],
    loading: false,
    exhausted: true,
    loadError: false,
    loadInitial: vi.fn(),
    markStale: vi.fn(),
    refreshIfStale: vi.fn().mockResolvedValue(undefined),
    loadMore: vi.fn(),
    retry: vi.fn(),
    create: vi.fn(),
    rename: vi.fn(),
    delete: vi.fn(),
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

describe("DirectMediaDetail", () => {
  const baseRaw = {
    id: "abc-123",
    timestamp: "2024-06-15T14:30:22Z",
    width: 3,
    height: 2,
    thumb_version: 3,
  };

  it("renders Location row when location_label is set", () => {
    const store = storeWith({
      ...baseRaw,
      latitude: 48.8566,
      longitude: 2.3522,
      location_label: "Paris, Île-de-France, France",
    });
    const { getByText } = render(DirectMediaDetail, {
      props: {
        id: "abc-123",
        mediaStore: store,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore(),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    expect(getByText("Location")).toBeTruthy();
    expect(getByText("Paris, Île-de-France, France")).toBeTruthy();
    expect(getByText("48.8566° N, 2.3522° E")).toBeTruthy();
  });

  it("renders coords-only when label is absent", () => {
    const store = storeWith({ ...baseRaw, latitude: 48.8566, longitude: 2.3522 });
    const { getByText, queryByText } = render(DirectMediaDetail, {
      props: {
        id: "abc-123",
        mediaStore: store,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore(),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    expect(getByText("Location")).toBeTruthy();
    expect(getByText("48.8566° N, 2.3522° E")).toBeTruthy();
    expect(queryByText(/Paris/)).toBeNull();
  });

  it("renders no Location row when neither label nor coords", () => {
    const store = storeWith(baseRaw);
    const { queryByText } = render(DirectMediaDetail, {
      props: {
        id: "abc-123",
        mediaStore: store,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore(),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    expect(queryByText("Location")).toBeNull();
  });

  it("re-fetches when id prop changes", async () => {
    // App.svelte mounts MediaDetail without a {#key} wrapper, so
    // navigating from one /media/:id to another reuses this component
    // instance. The id-keyed $effect must re-fetch the new (uncached)
    // row; the previous onMount-only path would have left the second
    // navigation stuck on Loading… Build the response body from the
    // requested URL so each id caches under its own key — otherwise
    // the second render would hit the store and short-circuit the
    // effect, masking the regression we're guarding against.
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation((input) => {
        const url = typeof input === "string" ? input : (input as Request).url;
        const id = url.split("/").pop() ?? "unknown";
        return Promise.resolve(
          new Response(
            JSON.stringify({
              id,
              timestamp: "2024-06-15T14:30:22Z",
              width: 1,
              height: 1,
              thumb_version: 1,
            }),
            { status: 200 },
          ),
        );
      });
    const store = new MediaStore({ GET: vi.fn() } as never);
    const albumsStore = makeAlbumsStore();
    const hiddenStore = makeHiddenStore();
    const toastStore = makeToastStore();
    const appConfig = defaultAppConfig();
    // First nav: id=first, no cached row → triggers fetch.
    const first = render(DirectMediaDetail, {
      props: { id: "first", mediaStore: store, albumsStore, hiddenStore, toastStore, appConfig },
    });
    // Wait for the first fetch to settle.
    await new Promise((r) => setTimeout(r, 0));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/media/first");
    fetchMock.mockClear();
    // Second nav: id=second, also uncached → must trigger another fetch.
    await first.rerender({ id: "second", mediaStore: store, albumsStore, hiddenStore, toastStore, appConfig });
    await new Promise((r) => setTimeout(r, 0));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/media/second");
    fetchMock.mockRestore();
  });

  // --- Hide/Unhide flows ---

  it("visible media shows Hide button when hiddenConfigured=true", () => {
    const store = storeWith(baseRaw);
    const { getByRole } = render(DirectMediaDetail, {
      props: {
        id: "abc-123",
        mediaStore: store,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore({ configured: true }),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    expect(getByRole("button", { name: "Hide" })).not.toBeNull();
  });

  it("visible media shows no Unhide when hiddenConfigured=true", () => {
    const store = storeWith(baseRaw);
    const { queryByRole } = render(DirectMediaDetail, {
      props: {
        id: "abc-123",
        mediaStore: store,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore({ configured: true }),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    expect(queryByRole("button", { name: "Unhide" })).toBeNull();
  });

  it("visible hide calls hiddenStore.hide and navigates to /library", async () => {
    const store = storeWith(baseRaw);
    const hide = vi.fn().mockResolvedValue({ succeeded: ["abc-123"], failed: [] });
    const { getByRole } = render(DirectMediaDetail, {
      props: {
        id: "abc-123",
        mediaStore: store,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore({ configured: true, hide }),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });
    vi.stubGlobal("confirm", () => true);
    await fireEvent.click(getByRole("button", { name: "Hide" }));
    await new Promise((r) => setTimeout(r, 0));
    expect(hide).toHaveBeenCalledWith(["abc-123"]);
    vi.unstubAllGlobals();
  });

  it("DirectMediaDetail hidden→unhide clones raw with hidden_at=null and calls mergeRaw", async () => {
    // Build a store WITHOUT the item (simulates hidden: store skips it).
    const store = new MediaStore({ GET: vi.fn() } as never);
    const mergeRawSpy = vi.spyOn(store, "mergeRaw");
    const unhide = vi.fn().mockResolvedValue({ succeeded: ["h1"], failed: [] });

    // Provide the raw via a fetch mock that returns hidden_at.
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          id: "h1",
          timestamp: "2024-01-01T00:00:00Z",
          width: 1,
          height: 1,
          thumb_version: 0,
          hidden_at: "2024-02-01T00:00:00Z",
        }),
        { status: 200 },
      ),
    );

    const { getByRole } = render(DirectMediaDetail, {
      props: {
        id: "h1",
        mediaStore: store,
        albumsStore: makeAlbumsStore(),
        hiddenStore: makeHiddenStore({ configured: true, unlocked: true, unhide }),
        toastStore: makeToastStore(),
        appConfig: defaultAppConfig(),
      },
    });

    // Wait for fetch to settle and lastRaw to be set.
    await new Promise((r) => setTimeout(r, 0));

    vi.stubGlobal("confirm", () => true);
    // Unhide button should be visible (isHidden=true from lastRaw)
    const unhideBtn = getByRole("button", { name: "Unhide" });
    await fireEvent.click(unhideBtn);
    await new Promise((r) => setTimeout(r, 0));

    expect(unhide).toHaveBeenCalledWith(["h1"]);
    // mergeRaw should have been called with hidden_at=null
    const calls = mergeRawSpy.mock.calls;
    const lastCall = calls[calls.length - 1];
    expect(lastCall).toBeDefined();
    const items = lastCall![0] as Array<Record<string, unknown>>;
    expect(items[0]?.["hidden_at"]).toBeNull();

    fetchMock.mockRestore();
    vi.unstubAllGlobals();
  });
});

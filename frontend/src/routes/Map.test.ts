import { render, screen, waitFor } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import Map from "./Map.svelte";
import { GeoStore } from "../lib/map/geoStore.svelte";
import type { Client } from "../lib/api/client";
import type { MediaStore } from "../lib/media/mediaStore.svelte";
import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
import type { ToastStore } from "../lib/toasts/toastStore.svelte";

function makeGeoStore(items: unknown[]): GeoStore {
  const client = {
    GET: async () => ({ data: { items } }),
  } as unknown as Pick<Client, "GET">;
  return new GeoStore(client);
}

// makeSequencedGeoStore returns a GeoStore whose underlying GET resolves
// to the next response in `responses` per call. Used by F7 tests where
// the visible-only fetch and the include_hidden retry must return
// different item sets.
function makeSequencedGeoStore(
  responses: Array<{ items: unknown[] }>,
): { store: GeoStore; calls: Array<{ params?: unknown }> } {
  const calls: Array<{ params?: unknown }> = [];
  let i = 0;
  const client = {
    GET: async (_path: string, opts: Record<string, unknown> = {}) => {
      calls.push({ params: (opts as { params?: unknown }).params });
      const r = responses[Math.min(i, responses.length - 1)];
      i++;
      return { data: r };
    },
  } as unknown as Pick<Client, "GET">;
  return { store: new GeoStore(client), calls };
}

function stubMediaStore(): MediaStore {
  return {} as unknown as MediaStore;
}
function stubHiddenStore(unlocked = false): HiddenStore {
  return { configured: unlocked, unlocked } as unknown as HiddenStore;
}
function stubToastStore(): ToastStore {
  const items: { id: string; message: string; kind?: string }[] = [];
  return {
    push: (t: { message: string; kind?: string }) => {
      const id = String(items.length);
      items.push({ id, ...t });
      return id;
    },
    items,
  } as unknown as ToastStore;
}

function mapProps(geoStore: GeoStore) {
  // The four route-derived params mirror the App.svelte call site: the
  // router emits `T | undefined`, so the prop types accept undefined and
  // tests must supply each key explicitly (exactOptionalPropertyTypes).
  return {
    z: undefined,
    c: undefined,
    focus: undefined,
    tab: undefined,
    geoStore,
    mediaStore: stubMediaStore(),
    hiddenStore: stubHiddenStore(),
    toastStore: stubToastStore(),
  };
}

describe("Map page shell", () => {
  it("renders the empty state when geo response has no items", async () => {
    const geoStore = makeGeoStore([]);
    render(Map, { props: mapProps(geoStore) });
    await waitFor(() =>
      expect(screen.getByText(/no geotagged photos/i)).toBeTruthy(),
    );
  });

  it("mounts MapPane in the loaded branch when geo response has items", async () => {
    const geoStore = makeGeoStore([
      {
        id: "a",
        timestamp: "2024-06-15T14:30:22Z",
        width: 1,
        height: 1,
        thumb_version: 0,
        latitude: 1,
        longitude: 2,
      },
    ]);
    render(Map, { props: mapProps(geoStore) });
    // Asserting on the map-loaded grid wrapper (not Leaflet internals)
    // — JSDOM can't render tiles, so the smoke test stops at "the
    // 60/40 split is in the DOM and MapPane mounted".
    await waitFor(() => expect(screen.getByTestId("map-loaded")).toBeTruthy());
    expect(screen.getByTestId("map-pane")).toBeTruthy();
  });
});

describe("Map page focus retry", () => {
  const hiddenItem = {
    id: "hidden-1",
    timestamp: "2024-06-15T14:30:22Z",
    width: 1,
    height: 1,
    thumb_version: 0,
    latitude: 1,
    longitude: 2,
    hidden_at: "2024-06-15T14:30:22Z",
  };

  it("retries with include_hidden=true when focus is missing and hidden is unlocked", async () => {
    const { store, calls } = makeSequencedGeoStore([
      { items: [] },
      { items: [hiddenItem] },
    ]);
    const toast = stubToastStore();
    const pushSpy = vi.spyOn(toast, "push");
    render(Map, {
      props: {
        ...mapProps(store),
        focus: "hidden-1",
        hiddenStore: stubHiddenStore(true),
        toastStore: toast,
      },
    });
    await waitFor(() => expect(calls.length).toBe(2));
    // First call: no params (visible-only). Second call: include_hidden=true.
    expect(calls[0]?.params).toBeUndefined();
    expect(calls[1]?.params).toMatchObject({ query: { include_hidden: true } });
    // Photo was found on retry → no toast.
    expect(pushSpy).not.toHaveBeenCalled();
  });

  it("does NOT retry when focus is missing but hidden is locked", async () => {
    const { store, calls } = makeSequencedGeoStore([
      { items: [] },
      { items: [hiddenItem] },
    ]);
    render(Map, {
      props: {
        ...mapProps(store),
        focus: "hidden-1",
        hiddenStore: stubHiddenStore(false),
      },
    });
    // Allow microtasks to flush.
    await new Promise((r) => setTimeout(r, 0));
    expect(calls.length).toBe(1);
  });

  it("toasts when focus stays missing after the include_hidden retry", async () => {
    const { store, calls } = makeSequencedGeoStore([
      { items: [] },
      { items: [] },
    ]);
    const toast = stubToastStore();
    const pushSpy = vi.spyOn(toast, "push");
    render(Map, {
      props: {
        ...mapProps(store),
        focus: "hidden-1",
        hiddenStore: stubHiddenStore(true),
        toastStore: toast,
      },
    });
    await waitFor(() => expect(calls.length).toBe(2));
    await waitFor(() => expect(pushSpy).toHaveBeenCalled());
    expect(pushSpy).toHaveBeenCalledWith({
      kind: "info",
      message: "Photo not found on map.",
    });
  });
});

describe("Map page hidden-include toggle", () => {
  const visibleItem = {
    id: "v1",
    timestamp: "2024-06-15T14:30:22Z",
    width: 1,
    height: 1,
    thumb_version: 0,
    latitude: 1,
    longitude: 2,
  };

  it("does not render the hidden toggle when locked", async () => {
    const geoStore = makeGeoStore([visibleItem]);
    render(Map, {
      props: { ...mapProps(geoStore), hiddenStore: stubHiddenStore(false) },
    });
    await waitFor(() => expect(screen.getByTestId("map-loaded")).toBeTruthy());
    expect(screen.queryByLabelText(/include hidden/i)).toBeNull();
  });

  it("renders the hidden toggle when unlocked", async () => {
    const geoStore = makeGeoStore([visibleItem]);
    const { findByLabelText } = render(Map, {
      props: { ...mapProps(geoStore), hiddenStore: stubHiddenStore(true) },
    });
    expect(await findByLabelText(/include hidden/i)).toBeTruthy();
  });

  it("re-fetches with include_hidden=true when the toggle flips on", async () => {
    const { store, calls } = makeSequencedGeoStore([
      { items: [visibleItem] },
      { items: [visibleItem] },
    ]);
    const { findByLabelText } = render(Map, {
      props: { ...mapProps(store), hiddenStore: stubHiddenStore(true) },
    });
    const cb = (await findByLabelText(/include hidden/i)) as HTMLInputElement;
    expect(calls.length).toBe(1);
    expect(calls[0]?.params).toBeUndefined();
    cb.click();
    await waitFor(() => expect(calls.length).toBe(2));
    expect(calls[1]?.params).toMatchObject({ query: { include_hidden: true } });
  });
});

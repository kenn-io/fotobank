import { render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import L from "leaflet";
import "leaflet.markercluster";
import Map from "./Map.svelte";
import { GeoStore } from "../lib/map/geoStore.svelte";
import type { Client } from "../lib/api/client";
import type { MediaStore } from "../lib/media/mediaStore.svelte";
import type { HiddenStore } from "../lib/hidden/hiddenStore.svelte";
import type { ToastStore } from "../lib/toasts/toastStore.svelte";

// VirtualGrid (mounted when MapGridPane has activeIds) wires
// ResizeObserver + IntersectionObserver in $effect blocks; jsdom ships
// neither. Same noop pattern used in MapGridPane.test.ts.
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
    takeRecords(): IntersectionObserverEntry[] {
      return [];
    }
  }
  vi.stubGlobal("ResizeObserver", NoopResizeObserver);
  vi.stubGlobal("IntersectionObserver", NoopIntersectionObserver);
});

afterEach(() => vi.restoreAllMocks());

function makeGeoStore(items: unknown[]): GeoStore {
  const client = {
    GET: async () => ({ data: { items } }),

hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); },
listMediaGeo(params?: any, options?: any) { return (this as any).GET("/api/v1/media/geo", { params: { query: params }, ...options }); }
} as unknown as Client;
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

hiddenState(options?: any) { return (this as any).GET("/api/v1/auth/hidden/state", { ...options }); },
listMedia(params?: any, options?: any) { return (this as any).GET("/api/v1/media", { params: { query: params }, ...options }); },
listMediaGeo(params?: any, options?: any) { return (this as any).GET("/api/v1/media/geo", { params: { query: params }, ...options }); }
} as unknown as Client;
  return { store: new GeoStore(client), calls };
}

function stubMediaStore(): MediaStore & { mergeRaw: ReturnType<typeof vi.fn> } {
  return {
    mergeRaw: vi.fn(),
    get: () => undefined,
    months: [],
  } as unknown as MediaStore & { mergeRaw: ReturnType<typeof vi.fn> };
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
  // SF-19 added activeFilters / tagLabels / onFiltersChange — tests
  // default to the empty filter set so existing focus / hidden /
  // tabs assertions remain unaffected.
  return {
    z: undefined,
    c: undefined,
    focus: undefined,
    tab: undefined,
    geoStore,
    mediaStore: stubMediaStore(),
    hiddenStore: stubHiddenStore(),
    toastStore: stubToastStore(),
    activeFilters: {
      cameras: [],
      lenses: [],
      tagKeys: [],
      hasGps: null,
      mediaType: null,
    },
    tagLabels: {},
    onFiltersChange: () => {},
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
    expect(calls[0]?.params).toEqual({ query: {} });
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
    expect(calls[0]?.params).toEqual({ query: {} });
    cb.click();
    await waitFor(() => expect(calls.length).toBe(2));
    expect(calls[1]?.params).toMatchObject({ query: { include_hidden: true } });
  });
});

describe("Map page mobile tabs", () => {
  const visibleItem = {
    id: "v1",
    timestamp: "2024-06-15T14:30:22Z",
    width: 1,
    height: 1,
    thumb_version: 0,
    latitude: 1,
    longitude: 2,
  };

  it("renders both tabs and switches active state on click", async () => {
    const geoStore = makeGeoStore([visibleItem]);
    const { findByRole } = render(Map, { props: mapProps(geoStore) });
    const mapTab = await findByRole("button", { name: /^map$/i });
    const photosTab = await findByRole("button", { name: /^photos$/i });
    expect(mapTab.classList.contains("active")).toBe(true);
    expect(photosTab.classList.contains("active")).toBe(false);
    photosTab.click();
    await waitFor(() => expect(photosTab.classList.contains("active")).toBe(true));
    expect(mapTab.classList.contains("active")).toBe(false);
  });

  it("seeds activeTab from the ?tab= prop when present", async () => {
    const geoStore = makeGeoStore([visibleItem]);
    const { findByRole } = render(Map, {
      props: { ...mapProps(geoStore), tab: "photos" as const },
    });
    const photosTab = await findByRole("button", { name: /^photos$/i });
    expect(photosTab.classList.contains("active")).toBe(true);
  });
});

describe("Map page mediaStore merge", () => {
  // Regression: MapGridPane filters activeIds via mediaStore.get(id) and
  // silently drops misses. /media/geo returns every geotagged primary,
  // but those rows aren't loaded into MediaStore until the library
  // timeline pages them in. Without this merge, the right-side grid
  // appears blank for IDs outside the loaded library page even though
  // their markers are on the map.
  const visibleItem = {
    id: "v1",
    timestamp: "2024-06-15T14:30:22Z",
    width: 1,
    height: 1,
    thumb_version: 0,
    latitude: 1,
    longitude: 2,
  };

  it("merges geoStore.rawItems into mediaStore after the initial load", async () => {
    const geoStore = makeGeoStore([visibleItem]);
    const mediaStore = stubMediaStore();
    const { findByTestId } = render(Map, {
      props: { ...mapProps(geoStore), mediaStore },
    });
    await findByTestId("map-loaded");
    await waitFor(() => expect(mediaStore.mergeRaw).toHaveBeenCalled());
    expect(mediaStore.mergeRaw).toHaveBeenCalledWith([visibleItem]);
  });
});

describe("Map page filter changes", () => {
  // Regression: when the filter set changes, the in-memory cluster
  // selection (clusterIds) must reset alongside the geo re-fetch.
  // Without the reset, MapGridPane keeps using the prior set of IDs
  // (it prefers clusterIds over viewportIds), so the right-grid renders
  // rows from the previous filter set even though the map markers now
  // reflect the new one.
  const visibleItem = {
    id: "v1",
    timestamp: "2024-06-15T14:30:22Z",
    width: 1,
    height: 1,
    thumb_version: 0,
    latitude: 1,
    longitude: 2,
  };

  it("re-fetches /media/geo with new params when activeFilters changes", async () => {
    const { store, calls } = makeSequencedGeoStore([
      { items: [visibleItem] },
      { items: [visibleItem] },
    ]);
    const baseProps = mapProps(store);
    const { rerender } = render(Map, { props: baseProps });
    // Initial load: no filter params, no query passed.
    await waitFor(() => expect(calls.length).toBe(1));
    expect(calls[0]?.params).toEqual({ query: {} });

    // Re-render with a populated filter set; the change-effect must
    // re-fire geoStore.load with the new wire shape.
    await rerender({
      ...baseProps,
      activeFilters: {
        cameras: ["Sony A7R IV"],
        lenses: [],
        tagKeys: [],
        hasGps: null,
        mediaType: null,
      },
    });
    await waitFor(() => expect(calls.length).toBe(2));
    expect(calls[1]?.params).toMatchObject({
      query: { camera: ["Sony A7R IV"] },
    });
  });

  // Pins the clusterIds reset itself, not just the re-fetch. The
  // observable signal is MapGridPane's "× Clear filter" chip, which
  // renders iff `clusterIds !== null`. A real cluster click in JSDOM
  // is impractical (Leaflet doesn't lay out tiles, so clusters never
  // form), so the test fires the markercluster plugin's
  // `clusterclick` event manually against the cluster group MapPane
  // mounts during onMount — that's the same handler path the
  // production wiring reaches.
  it("resets clusterIds when activeFilters changes", async () => {
    // Two GPS rows at the same coords so they reliably land in one
    // cluster — the clusterclick handler maps each child marker back
    // to its id via MapPane's idByMarker, so the markers must come
    // from the rendered geo set.
    const item2 = { ...visibleItem, id: "v2" };
    const { store, calls } = makeSequencedGeoStore([
      { items: [visibleItem, item2] },
      { items: [visibleItem, item2] },
    ]);
    const baseProps = mapProps(store);

    // Capture the L.Map instance MapPane mounts so we can reach its
    // cluster group and fire `clusterclick` manually. addLayer is
    // called once with the cluster group during MapPane.onMount.
    let mapInstance: L.Map | null = null;
    const origAddLayer = L.Map.prototype.addLayer;
    const addLayerSpy = vi
      .spyOn(L.Map.prototype, "addLayer")
      .mockImplementation(function (this: L.Map, layer: L.Layer) {
        if (mapInstance === null) mapInstance = this;
        return origAddLayer.call(this, layer);
      });

    const { rerender, queryByText } = render(Map, { props: baseProps });
    // Wait for both the geo fetch AND MapPane's onMount → addLayer to
    // run before restoring the spy. Just waiting on `calls.length===1`
    // races: the /geo call resolves before Svelte schedules MapPane's
    // mount, so a spy restore can land before mapInstance is captured.
    await waitFor(() => expect(calls.length).toBe(1));
    await waitFor(() => expect(mapInstance).not.toBeNull());

    // Restore addLayer so we don't perturb downstream Leaflet ops.
    addLayerSpy.mockRestore();
    // The waitFor above proves mapInstance is non-null at runtime, but
    // TS can't narrow through expect(...).not.toBeNull(). The explicit
    // throw is the type guard the rest of this test relies on.
    if (mapInstance === null) throw new Error("map instance was not captured");

    // Find the MarkerClusterGroup the MapPane added.
    let cluster: L.MarkerClusterGroup | null = null;
    (mapInstance as L.Map).eachLayer((layer) => {
      const ctor = (L as unknown as { MarkerClusterGroup: new () => unknown })
        .MarkerClusterGroup;
      if (layer instanceof ctor) cluster = layer as L.MarkerClusterGroup;
    });
    if (cluster === null) throw new Error("cluster group not found on map");
    const cg = cluster as L.MarkerClusterGroup;
    const childMarkers = cg.getLayers() as L.Marker[];
    expect(childMarkers.length).toBe(2);

    // No clear-chip yet — clusterIds starts null.
    expect(queryByText(/clear filter/i)).toBeNull();

    // Force the popup branch (cluster won't split) so the
    // clusterclick handler doesn't trigger fitBounds + state changes
    // we don't care about; we only need clusterIds to flip non-null.
    vi.spyOn(L.Map.prototype, "getBoundsZoom").mockReturnValue(12);
    vi.spyOn(L.Map.prototype, "getZoom").mockReturnValue(12);

    (cg as unknown as L.Evented).fire("clusterclick", {
      layer: {
        getAllChildMarkers: () => childMarkers,
        getBounds: () =>
          L.latLngBounds([
            [1, 2],
            [1, 2],
          ]),
        getLatLng: () => L.latLng(1, 2),
      },
    });
    await waitFor(() => expect(queryByText(/clear filter/i)).not.toBeNull());

    // Filter mutation must reset clusterIds → clear-chip disappears.
    await rerender({
      ...baseProps,
      activeFilters: {
        cameras: ["Sony A7R IV"],
        lenses: [],
        tagKeys: [],
        hasGps: null,
        mediaType: null,
      },
    });
    await waitFor(() => expect(queryByText(/clear filter/i)).toBeNull());
  });
});

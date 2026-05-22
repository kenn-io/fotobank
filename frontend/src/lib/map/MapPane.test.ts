import { render } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import L from "leaflet";
import "leaflet.markercluster";
import MapPane from "./MapPane.svelte";
import type { Media } from "../media/mediaStore.svelte";

// Heavier behavior — moveend/zoomend, real cluster splitting at zoom
// thresholds — is covered by Playwright e2e where Leaflet runs against
// a real DOM. JSDOM doesn't lay out the map container, so we can't
// trigger natural cluster formation. The tests below capture the map
// instance via a transient `L.Map.prototype.addLayer` spy, then fire
// `clusterclick` manually with a fake `MarkerCluster`-shaped layer.
// That's the same wiring the production handler sees from Leaflet's
// own dispatcher.

afterEach(() => vi.restoreAllMocks());

describe("MapPane mount", () => {
  it("renders an empty container when items is empty", () => {
    const { getByTestId } = render(MapPane, {
      props: emptyMapPaneProps(),
    });
    const el = getByTestId("map-pane");
    expect(el).toBeTruthy();
  });
});

describe("MapPane cluster click", () => {
  it("disables markercluster spiderfy behavior", () => {
    const { cg } = mountAndCaptureCluster();
    const options = cg.options as MarkerClusterClickOptions;

    expect(options.spiderfyOnMaxZoom).toBe(false);
    expect(Object.hasOwn(options, "spiderfyOnEveryZoom")).toBe(true);
    expect(options.spiderfyOnEveryZoom).toBe(false);
    expect(options.zoomToBoundsOnClick).toBe(false);
  });

  it("zoom branch: targetZoom > current+0.5 → fitBounds, no popup", () => {
    const { onClusterClick, cg, childMarkers } = mountAndCaptureCluster();
    // getBoundsZoom > getZoom() + 0.5 → zoom branch
    vi.spyOn(L.Map.prototype, "getBoundsZoom").mockReturnValue(18);
    vi.spyOn(L.Map.prototype, "getZoom").mockReturnValue(12);
    const fitBoundsSpy = vi.spyOn(L.Map.prototype, "fitBounds");
    fitBoundsSpy.mockClear();

    fireClusterClick(cg, childMarkers);

    // Right-grid filter callback always fires (matches existing F5 behavior)
    expect(onClusterClick).toHaveBeenCalledTimes(1);
    expect(onClusterClick.mock.calls[0]?.[0]).toEqual(["a", "b"]);
    // Zoom path took fitBounds, not popup
    expect(fitBoundsSpy).toHaveBeenCalled();
    expect(document.querySelectorAll(".leaflet-popup").length).toBe(0);
  });

  it("popup branch: targetZoom <= current+0.5 → popup gallery, no fitBounds", () => {
    const { cg, childMarkers } = mountAndCaptureCluster();
    // getBoundsZoom <= getZoom() + 0.5 → popup branch (cluster won't split)
    vi.spyOn(L.Map.prototype, "getBoundsZoom").mockReturnValue(12);
    vi.spyOn(L.Map.prototype, "getZoom").mockReturnValue(12);
    const fitBoundsSpy = vi.spyOn(L.Map.prototype, "fitBounds");
    fitBoundsSpy.mockClear();

    fireClusterClick(cg, childMarkers);

    expect(fitBoundsSpy).not.toHaveBeenCalled();
    expect(document.querySelectorAll(".leaflet-popup").length).toBe(1);
    // Popup body renders the gallery cells with a data-id per child
    const cells = document.querySelectorAll<HTMLElement>(".map-cluster-popup__cell");
    expect(cells.length).toBe(2);
    const ids = [...cells].map((c) => c.dataset["id"]);
    expect(ids).toEqual(["a", "b"]);
  });

  it("popup cell click dispatches onMarkerClick with the cell's id", () => {
    const onMarkerClick = vi.fn();
    const { cg, childMarkers } = mountAndCaptureCluster({ onMarkerClick });
    // Force popup branch
    vi.spyOn(L.Map.prototype, "getBoundsZoom").mockReturnValue(12);
    vi.spyOn(L.Map.prototype, "getZoom").mockReturnValue(12);

    fireClusterClick(cg, childMarkers);

    const cell = document.querySelector<HTMLButtonElement>(
      '.map-cluster-popup__cell[data-id="b"]',
    );
    expect(cell).not.toBeNull();
    cell!.click();
    expect(onMarkerClick).toHaveBeenCalledWith("b");
  });

  // Pins the buildMarkers popup-cleanup contract: when items change
  // (e.g. a filter mutation or the F7 hidden-include retry lands a new
  // geo set), the cluster's child markers are detached during
  // clearLayers(); a popup that referenced those markers becomes
  // stale. buildMarkers must close it before clearing layers, otherwise
  // the user is left staring at thumb cells whose data-ids no longer
  // exist on the map.
  it("closes an open cluster popup when items change", async () => {
    const { cg, childMarkers, rerender } = mountAndCaptureCluster();
    vi.spyOn(L.Map.prototype, "getBoundsZoom").mockReturnValue(12);
    vi.spyOn(L.Map.prototype, "getZoom").mockReturnValue(12);

    fireClusterClick(cg, childMarkers);
    expect(document.querySelectorAll(".leaflet-popup").length).toBe(1);

    // Items change to a single different row — buildMarkers reruns,
    // clearLayers detaches the markers the popup referenced, and the
    // cleanup contract closes the popup.
    await rerender({
      items: [{ ...ITEMS[0]!, id: "z", latitude: 1, longitude: 2 }],
    });
    expect(document.querySelectorAll(".leaflet-popup").length).toBe(0);
  });
});

// ----------------------------------------------------------------------
// helpers

const ITEMS = [
  {
    id: "a",
    timestamp: "2024-06-15T14:30:22Z",
    aspect: 1,
    thumbUrl: "/api/v1/media/a/thumb?size=grid&v=0",
    thumbStatus: "ready" as const,
    taken: new Date(),
    thumbVersion: 0,
    latitude: 48.8566,
    longitude: 2.3522,
  },
  {
    id: "b",
    timestamp: "2024-06-15T14:30:22Z",
    aspect: 1,
    thumbUrl: "/api/v1/media/b/thumb?size=grid&v=0",
    thumbStatus: "ready" as const,
    taken: new Date(),
    thumbVersion: 0,
    latitude: 48.8566,
    longitude: 2.3522,
  },
];

function emptyMapPaneProps() {
  return {
    items: [],
    initialZoom: undefined,
    initialCenter: undefined,
    focusId: undefined,
    onMarkerClick: vi.fn(),
    onClusterClick: vi.fn(),
    onViewportChange: vi.fn(),
    onViewState: vi.fn(),
    onClearClusterFilter: vi.fn(),
  };
}

type CallbackOverrides = Partial<{
  onMarkerClick: ReturnType<typeof vi.fn>;
  onClusterClick: ReturnType<typeof vi.fn>;
}>;

type MarkerClusterClickOptions = L.LayerOptions & {
  spiderfyOnMaxZoom?: boolean;
  spiderfyOnEveryZoom?: boolean;
  zoomToBoundsOnClick?: boolean;
};

// mountAndCaptureCluster renders MapPane with two same-coord items and
// captures the L.Map + the L.MarkerClusterGroup added during onMount.
// The cluster is reachable via map.eachLayer; the addLayer spy is the
// minimum-friction way to grab the map instance Svelte holds privately.
function mountAndCaptureCluster(overrides: CallbackOverrides = {}): {
  onMarkerClick: ReturnType<typeof vi.fn>;
  onClusterClick: ReturnType<typeof vi.fn>;
  cg: L.MarkerClusterGroup;
  childMarkers: L.Marker[];
  rerender: (props: { items: Media[] }) => Promise<void>;
} {
  let mapInstance: L.Map | null = null;
  const origAddLayer = L.Map.prototype.addLayer;
  vi.spyOn(L.Map.prototype, "addLayer").mockImplementation(function (
    this: L.Map,
    layer: L.Layer,
  ) {
    if (mapInstance === null) mapInstance = this;
    return origAddLayer.call(this, layer);
  });

  const onMarkerClick = overrides.onMarkerClick ?? vi.fn();
  const onClusterClick = overrides.onClusterClick ?? vi.fn();

  const baseProps = {
    items: ITEMS,
    initialZoom: 12,
    initialCenter: [48.8566, 2.3522] as [number, number],
    focusId: undefined,
    onMarkerClick,
    onClusterClick,
    onViewportChange: vi.fn(),
    onViewState: vi.fn(),
    onClearClusterFilter: vi.fn(),
  };
  const { rerender: rerenderRaw } = render(MapPane, { props: baseProps });

  if (mapInstance === null) throw new Error("map instance was not captured");
  let cluster: L.MarkerClusterGroup | null = null;
  (mapInstance as L.Map).eachLayer((layer) => {
    // markercluster's namespace exports MarkerClusterGroup off `L`,
    // but the type-only import doesn't carry the constructor, so a
    // runtime instanceof against the augmented namespace is the only
    // way to filter.
    const ctor = (L as unknown as { MarkerClusterGroup: new () => unknown })
      .MarkerClusterGroup;
    if (layer instanceof ctor) {
      cluster = layer as L.MarkerClusterGroup;
    }
  });
  if (cluster === null) throw new Error("MarkerClusterGroup was not found on map");
  const cg = cluster as L.MarkerClusterGroup;
  const childMarkers = cg.getLayers() as L.Marker[];
  const rerender = async (next: { items: Media[] }): Promise<void> => {
    await rerenderRaw({ ...baseProps, ...next });
  };
  return { onMarkerClick, onClusterClick, cg, childMarkers, rerender };
}

// fireClusterClick mimics what the markercluster plugin emits when a
// real cluster is clicked: a `clusterclick` event whose `layer` is a
// MarkerCluster with getAllChildMarkers/getBounds/getLatLng. JSDOM
// doesn't lay the map out so a real cluster never forms — firing the
// event manually exercises the same handler path.
function fireClusterClick(
  cg: L.MarkerClusterGroup,
  childMarkers: L.Marker[],
): void {
  const fakeLayer = {
    getAllChildMarkers: () => childMarkers,
    getBounds: () => L.latLngBounds([[48.8566, 2.3522], [48.8566, 2.3522]]),
    getLatLng: () => L.latLng(48.8566, 2.3522),
  };
  (cg as unknown as L.Evented).fire("clusterclick", { layer: fakeLayer });
}

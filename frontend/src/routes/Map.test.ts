import { render, screen, waitFor } from "@testing-library/svelte";
import { describe, expect, it } from "vitest";
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

function stubMediaStore(): MediaStore {
  return {} as unknown as MediaStore;
}
function stubHiddenStore(): HiddenStore {
  return { configured: false, unlocked: false } as unknown as HiddenStore;
}
function stubToastStore(): ToastStore {
  return { push: () => "", items: [] } as unknown as ToastStore;
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

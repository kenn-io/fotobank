import { render, screen } from "@testing-library/svelte";
import { describe, expect, it, vi, beforeAll } from "vitest";
import MapGridPane from "./MapGridPane.svelte";
import { MediaStore } from "../media/mediaStore.svelte";
import { GeoStore } from "./geoStore.svelte";
import type { Client } from "../api/client";

// VirtualGrid (rendered when activeIds is non-empty) wires
// ResizeObserver + IntersectionObserver in $effect blocks; jsdom
// ships neither. Same noop pattern used in AlbumDetail.test.ts and
// VirtualGrid.flat.test.ts — assertions don't need observation
// callbacks to fire.
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

function stubMediaStore(): MediaStore {
  // Real MediaStore with a no-op fake client; the empty-state and
  // clear-chip assertions don't trigger network paths, so the GET
  // is never invoked.
  const client = { GET: vi.fn() } as unknown as Pick<Client, "GET">;
  return new MediaStore(client);
}

function stubGeoStore(): GeoStore {
  const client = { GET: vi.fn() } as unknown as Pick<Client, "GET">;
  return new GeoStore(client);
}

describe("MapGridPane", () => {
  it("shows the empty message when activeIds is empty", () => {
    render(MapGridPane, {
      props: {
        visibleIds: [],
        clusterIds: null,
        onPhotoClick: vi.fn(),
        onClearClusterFilter: vi.fn(),
        mediaStore: stubMediaStore(),
        geoStore: stubGeoStore(),
      },
    });
    expect(screen.getByText(/no photos in view/i)).toBeTruthy();
  });

  it("shows the clear-filter chip when a clusterIds filter is active", () => {
    render(MapGridPane, {
      props: {
        visibleIds: ["a", "b"],
        clusterIds: ["a"],
        onPhotoClick: vi.fn(),
        onClearClusterFilter: vi.fn(),
        mediaStore: stubMediaStore(),
        geoStore: stubGeoStore(),
      },
    });
    expect(screen.getByText(/× Clear filter/)).toBeTruthy();
  });

  it("omits the clear-filter chip when clusterIds is null", () => {
    render(MapGridPane, {
      props: {
        visibleIds: [],
        clusterIds: null,
        onPhotoClick: vi.fn(),
        onClearClusterFilter: vi.fn(),
        mediaStore: stubMediaStore(),
        geoStore: stubGeoStore(),
      },
    });
    expect(screen.queryByText(/× Clear filter/)).toBeNull();
  });
});

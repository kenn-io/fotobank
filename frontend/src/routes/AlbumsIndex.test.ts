import { fireEvent, render } from "@testing-library/svelte";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AlbumsStore } from "../lib/albums/albumsStore.svelte";
import AlbumsIndex from "./AlbumsIndex.svelte";

class NoopIntersectionObserver implements IntersectionObserver {
  readonly root = null;
  readonly rootMargin = "";
  readonly thresholds = [];
  disconnect() {}
  observe() {}
  takeRecords(): IntersectionObserverEntry[] {
    return [];
  }
  unobserve() {}
}

function emptyStore(): AlbumsStore {
  return {
    albums: [],
    loading: false,
    exhausted: true,
    loadError: false,
    loadInitial: vi.fn(),
    loadMore: vi.fn(),
    refreshIfStale: vi.fn(),
    retry: vi.fn(),
    create: vi.fn(),
  } as unknown as AlbumsStore;
}

describe("AlbumsIndex", () => {
  beforeEach(() => {
    vi.stubGlobal("IntersectionObserver", NoopIntersectionObserver);
  });

  it("offers an explicit close control in the new-album dialog", async () => {
    const { getByRole, queryByRole } = render(AlbumsIndex, {
      props: { albumsStore: emptyStore() },
    });

    await fireEvent.click(
      getByRole("button", { name: "Create your first album" }),
    );
    expect(getByRole("dialog", { name: "New album" })).toBeTruthy();

    await fireEvent.click(getByRole("button", { name: "Close" }));
    expect(queryByRole("dialog", { name: "New album" })).toBeNull();
  });
});

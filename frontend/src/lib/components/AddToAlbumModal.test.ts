import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, vi } from "vitest";
import AddToAlbumModal from "./AddToAlbumModal.svelte";
import type { AlbumsStore } from "../albums/albumsStore.svelte";

function makeStore(albums: any[]): AlbumsStore {
  return {
    albums,
    loading: false,
    exhausted: true,
    create: vi.fn(),
    rename: vi.fn(),
    delete: vi.fn(),
    loadInitial: vi.fn(),
    loadMore: vi.fn(),
    byId: (id: string) => albums.find((a) => a.id === id),
  } as any;
}

const fakeAlbums = [
  { id: "a1", name: "Italy", item_count: 12, cover: null, created_at: "x", updated_at: "x" },
  { id: "a2", name: "Family", item_count: 5, cover: null, created_at: "x", updated_at: "x" },
];

describe("AddToAlbumModal", () => {
  it("primary button is disabled until a target album is selected", () => {
    const onAdd = vi.fn();
    const { getByRole } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1", "m2"],
        albumsStore: makeStore(fakeAlbums),
        onAdd,
        onClose: vi.fn(),
      },
    });
    const primary = getByRole("button", { name: /^Add 2 photos$/ });
    expect(primary.hasAttribute("disabled")).toBe(true);
  });

  it("clicking a row enables the primary button (does not auto-fire)", async () => {
    const onAdd = vi.fn();
    const { getByRole, getByText } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1", "m2"],
        albumsStore: makeStore(fakeAlbums),
        onAdd,
        onClose: vi.fn(),
      },
    });
    await fireEvent.click(getByText("Italy"));
    expect(onAdd).not.toHaveBeenCalled();
    const primary = getByRole("button", { name: /^Add 2 photos$/ });
    expect(primary.hasAttribute("disabled")).toBe(false);
  });

  it("clicking primary calls onAdd with selected album id", async () => {
    const onAdd = vi.fn().mockResolvedValue({ added: 2, already_present: 0 });
    const { getByRole, getByText } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1", "m2"],
        albumsStore: makeStore(fakeAlbums),
        onAdd,
        onClose: vi.fn(),
      },
    });
    await fireEvent.click(getByText("Italy"));
    await fireEvent.click(getByRole("button", { name: /^Add 2 photos$/ }));
    expect(onAdd).toHaveBeenCalledWith("a1");
  });

  it("filters albums by name (case-insensitive)", async () => {
    const { getByPlaceholderText, queryByText } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1"],
        albumsStore: makeStore(fakeAlbums),
        onAdd: vi.fn(),
        onClose: vi.fn(),
      },
    });
    const input = getByPlaceholderText("Search albums...");
    await fireEvent.input(input, { target: { value: "fam" } });
    expect(queryByText("Italy")).toBeNull();
    expect(queryByText("Family")).not.toBeNull();
  });

  it("create-new flow leaves modal in 'ready to add' state with new album selected", async () => {
    const store = makeStore(fakeAlbums);
    store.create = vi.fn(async (n: string) => {
      // Simulate a successful create that pushes the new album to the front.
      (store as any).albums = [{ id: "anew", name: n, item_count: 0, cover: null, created_at: "x", updated_at: "x" }, ...fakeAlbums];
    });
    const { getByText, getByRole, getByPlaceholderText } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1"],
        albumsStore: store,
        onAdd: vi.fn(),
        onClose: vi.fn(),
      },
    });
    await fireEvent.click(getByText("+ Create new album"));
    await fireEvent.input(getByPlaceholderText("Album name"), { target: { value: "Trip" } });
    await fireEvent.click(getByRole("button", { name: "Create" }));
    // After create, modal is back in list view with "Trip" highlighted.
    const primary = getByRole("button", { name: /^Add 1 photo$/ });
    expect(primary.hasAttribute("disabled")).toBe(false);
  });
});

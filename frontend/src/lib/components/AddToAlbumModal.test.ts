import { render, fireEvent } from "@testing-library/svelte";
import { describe, it, expect, vi } from "vitest";
import AddToAlbumModal from "./AddToAlbumModal.svelte";
import { AlbumsStore, type AlbumListItem } from "../albums/albumsStore.svelte";

// makeStore builds a partial AlbumsStore stub for tests that only need a
// frozen list. Tests that also need to react to mutations of `albums`
// (e.g. the create-new flow) must use a real AlbumsStore class instance
// instead — Svelte's $state proxy unwraps class instances but caches
// plain-object property reads, so reassigning `(stub as any).albums = …`
// does not propagate to the rendered component.
function makeStore(albums: AlbumListItem[]): AlbumsStore {
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
  } as unknown as AlbumsStore;
}

// AlbumListItem.cover is optional (not nullable) — the wire shape comes
// from huma's `omitempty`, so the field is absent rather than null when
// no cover is set. Match that shape in fixtures.
const fakeAlbums: AlbumListItem[] = [
  { id: "a1", name: "Italy", item_count: 12, created_at: "x", updated_at: "x" },
  { id: "a2", name: "Family", item_count: 5, created_at: "x", updated_at: "x" },
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

  it("create-new flow selects the created album by id, even when a same-name album already exists", async () => {
    // Use a real AlbumsStore wired to a fakeClient so the create →
    // refetch path mutates `albums` through the same setters production
    // uses. A hand-stubbed mock with `(store as any).albums = …` would
    // bypass Svelte's $state proxy on plain objects and the rendered
    // modal would never see the new album.
    //
    // Critical to this test: the seeded list ALREADY contains an album
    // named "Trip" (id "old-trip"). The user creates another "Trip" —
    // the modal must select the NEW album by id, not the older one a
    // name-match would resolve. This exercises the id-returning
    // AlbumsStore.create() contract that AddToAlbumModal depends on.
    const created = { id: "anew", name: "Trip", item_count: 0, created_at: "x", updated_at: "x" };
    const seededList = [
      { id: "old-trip", name: "Trip", item_count: 3, created_at: "x", updated_at: "x" },
      { id: "a1", name: "Italy", item_count: 12, cover: { media_id: "m1", thumb_version: 1 }, created_at: "x", updated_at: "x" },
      { id: "a2", name: "Family", item_count: 5, created_at: "x", updated_at: "x" },
    ];
    const refetchedList = [created, ...seededList];

    let getCalls = 0;
    const fakeClient = {
      GET: vi.fn(async () => {
        getCalls += 1;
        // First GET: seeded list (mount + initial loadInitial).
        // Subsequent GETs: post-create refetch with the new album at the front.
        return { data: { items: getCalls === 1 ? seededList : refetchedList, next_offset: null } };
      }),
      POST: vi.fn(async () => ({ data: created })),
      PATCH: vi.fn(async () => ({ data: null })),
      DELETE: vi.fn(async () => ({ data: null })),

listAlbums(params?: any, options?: any) { return (this as any).GET("/api/v1/albums", { params: { query: params }, ...options }); },
createAlbum(albumNameRequest?: any, options?: any) { return (this as any).POST("/api/v1/albums", { body: albumNameRequest, ...options }); },
deleteAlbum(id?: any, options?: any) { return (this as any).DELETE("/api/v1/albums/{id}", { params: { path: { id } }, ...options }); },
renameAlbum(id?: any, albumNameRequest?: any, options?: any) { return (this as any).PATCH("/api/v1/albums/{id}", { params: { path: { id } }, body: albumNameRequest, ...options }); }
};
    const store = new AlbumsStore(fakeClient as never);
    await store.loadInitial();

    const onAdd = vi.fn().mockResolvedValue({ added: 1, already_present: 0 });
    const { getByText, getByRole, findByRole, getByPlaceholderText } = render(AddToAlbumModal, {
      props: {
        mediaIds: ["m1"],
        albumsStore: store,
        onAdd,
        onClose: vi.fn(),
      },
    });
    await fireEvent.click(getByText("+ Create new album"));
    await fireEvent.input(getByPlaceholderText("Album name"), { target: { value: "Trip" } });
    await fireEvent.click(getByRole("button", { name: "Create" }));
    // The create flow awaits POST + loadInitial; findByRole polls until
    // the modal returns to list mode and the primary "Add 1 photo"
    // button appears.
    const primary = await findByRole("button", { name: /^Add 1 photo$/ });
    expect(primary.hasAttribute("disabled")).toBe(false);
    // The created id ("anew") must be selected, not the older same-name
    // "old-trip" that a name-match fallback would have resolved.
    await fireEvent.click(primary);
    expect(onAdd).toHaveBeenCalledWith("anew");
  });
});

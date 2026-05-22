import { describe, it, expect, vi } from "vitest";
import { AlbumDetailStore } from "./albumDetailStore.svelte";
import { MediaStore } from "../media/mediaStore.svelte";

function fakeClient(responses: Array<any>) {
  let i = 0;
  const calls: Array<{ method: string; path: string; opts: any }> = [];
  const handler = vi.fn(async (path: string, opts: any = {}) => {
    calls.push({ method: "h", path, opts });
    return responses[i++] ?? { data: null };
  });
  return { GET: handler, POST: handler, PATCH: handler, DELETE: handler, calls };
}

const fakeMedia = (id: string, ts = "2025-01-01T00:00:00Z") => ({
  id,
  timestamp: ts,
  width: 100,
  height: 100,
  thumb_version: 0,
});

describe("AlbumDetailStore.load", () => {
  it("fetches album metadata + first page of media and merges into MediaStore", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "Italy", item_count: 2, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2")], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    expect(store.album?.name).toBe("Italy");
    expect(store.itemIds).toEqual(["m1", "m2"]);
    expect(ms.get("m1")).not.toBeUndefined();
    expect(ms.get("m2")).not.toBeUndefined();
    expect(store.exhausted).toBe(true);
  });
});

describe("AlbumDetailStore.setSort", () => {
  it("resets pagination and refetches with the new sort", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 4, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2")], next_offset: 2 } },
      { data: { items: [fakeMedia("m3"), fakeMedia("m4")], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    expect(store.itemIds).toEqual(["m1", "m2"]);

    await store.setSort("added");
    const lastQuery = client.calls.at(-1)?.opts.params?.query;
    expect(lastQuery?.sort_by).toBe("added");
    expect(store.itemIds).toEqual(["m3", "m4"]);
  });
});

describe("AlbumDetailStore.removeMany", () => {
  it("returns succeeded + failed and removes successful IDs from itemIds", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 3, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2"), fakeMedia("m3")], next_offset: null } },
      { data: null },
      { error: { status: 500, message: "boom" } },
      { data: null },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    const result = await store.removeMany(["m1", "m2", "m3"]);
    expect(result.succeeded.sort()).toEqual(["m1", "m3"]);
    expect(result.failed).toEqual(["m2"]);
    expect(store.itemIds).toEqual(["m2"]);
  });
});

describe("AlbumDetailStore.hasInAlbum", () => {
  it("returns true only for IDs present in itemIds", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 1, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1")], next_offset: null } },
    ]);
    const store = new AlbumDetailStore(client as any, new MediaStore(client as any));
    await store.load("a1");
    expect(store.hasInAlbum("m1")).toBe(true);
    expect(store.hasInAlbum("m2")).toBe(false);
  });
});

describe("AlbumDetailStore parses hidden_count", () => {
  it("loads hidden_count from album metadata into store.album", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "Italy", item_count: 95, hidden_count: 5, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    expect(store.album?.hidden_count).toBe(5);
  });
});

describe("AlbumDetailStore.refreshMeta", () => {
  it("refetches album header and updates counts without resetting itemIds", async () => {
    const client = fakeClient([
      // initial load: meta + items
      { data: { id: "a1", name: "X", item_count: 3, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2"), fakeMedia("m3")], next_offset: null } },
      // refreshMeta: updated meta only
      { data: { id: "a1", name: "X", item_count: 2, hidden_count: 1, cover: null, created_at: "x", updated_at: "x" } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    expect(store.itemIds).toEqual(["m1", "m2", "m3"]);

    await store.refreshMeta();

    // header updated
    expect(store.album?.item_count).toBe(2);
    expect(store.album?.hidden_count).toBe(1);
    // itemIds NOT reset
    expect(store.itemIds).toEqual(["m1", "m2", "m3"]);
    // membership NOT reset
    expect(store.hasInAlbum("m1")).toBe(true);
  });

  it("is a no-op when no albumId is set", async () => {
    const client = fakeClient([]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.refreshMeta(); // should not throw
    expect(client.calls.length).toBe(0);
  });

  it("updates cover from API response (finding #10)", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 1, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1")], next_offset: null } },
      { data: { id: "a1", name: "X", item_count: 1, hidden_count: 0, cover: { media_id: "m1", thumb_version: 2 }, created_at: "x", updated_at: "y" } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    expect(store.album?.cover).toBeUndefined();

    await store.refreshMeta();
    expect(store.album?.cover?.media_id).toBe("m1");
    expect(store.album?.cover?.thumb_version).toBe(2);
  });

  it("drops stale response if album changed mid-flight (finding #11)", async () => {
    // Simulate: refreshMeta starts for a1, then load("a2") fires before
    // the response lands. The stale a1 response must not overwrite a2's data.
    let resolveStale!: (v: { data: any }) => void;
    const stalePromise = new Promise<{ data: any }>((r) => { resolveStale = r; });
    let callIdx = 0;
    const responses: Array<{ data: any } | Promise<{ data: any }>> = [
      // load("a1"): meta + items
      { data: { id: "a1", name: "A1", item_count: 1, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1")], next_offset: null } },
      // refreshMeta("a1"): held pending
      stalePromise,
      // load("a2"): meta + items
      { data: { id: "a2", name: "A2", item_count: 0, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [], next_offset: null } },
    ];
    const client = {
      GET: vi.fn(async () => responses[callIdx++] ?? { data: null }),
      PATCH: vi.fn(), DELETE: vi.fn(), POST: vi.fn(),
    };
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);

    await store.load("a1");
    expect(store.album?.name).toBe("A1");

    // Start refreshMeta while it will block on the pending promise
    const refresh = store.refreshMeta();
    // Navigate to a2 (bumps loadToken, changes albumId)
    await store.load("a2");
    expect(store.album?.name).toBe("A2");

    // Resolve the stale refreshMeta response now
    resolveStale({ data: { id: "a1", name: "A1-stale", item_count: 0, hidden_count: 0, cover: null, created_at: "x", updated_at: "z" } });
    await refresh;

    // The stale response must not have overwritten the a2 album.
    expect(store.album?.name).toBe("A2");
  });
});

describe("AlbumDetailStore.pruneHidden", () => {
  it("removes ids from itemIds and membership without calling DELETE", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 3, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2"), fakeMedia("m3")], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");
    const callsBefore = client.calls.length;

    store.pruneHidden(["m1", "m3"]);

    // No new HTTP calls
    expect(client.calls.length).toBe(callsBefore);
    // m1 and m3 removed from view
    expect(store.itemIds).toEqual(["m2"]);
    expect(store.hasInAlbum("m1")).toBe(false);
    expect(store.hasInAlbum("m3")).toBe(false);
    expect(store.hasInAlbum("m2")).toBe(true);
  });

  it("decrements item_count by the number of pruned ids", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 3, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2"), fakeMedia("m3")], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");

    store.pruneHidden(["m1", "m2"]);
    expect(store.album?.item_count).toBe(1);
  });

  it("increments hidden_count by the number of pruned ids", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 3, hidden_count: 2, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2"), fakeMedia("m3")], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");

    store.pruneHidden(["m1"]);
    expect(store.album?.hidden_count).toBe(3);
  });

  it("is a no-op when ids is empty", async () => {
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 2, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2")], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");

    store.pruneHidden([]);
    expect(store.itemIds).toEqual(["m1", "m2"]);
    expect(store.album?.item_count).toBe(2);
  });

  it("only decrements by the intersection with album membership (finding #14)", async () => {
    // Caller passes ids from a wider scope (e.g. global selection) that
    // includes items not in this album. Only the ones actually present
    // should affect item_count and hidden_count.
    const client = fakeClient([
      { data: { id: "a1", name: "X", item_count: 2, hidden_count: 0, cover: null, created_at: "x", updated_at: "x" } },
      { data: { items: [fakeMedia("m1"), fakeMedia("m2")], next_offset: null } },
    ]);
    const ms = new MediaStore(client as any);
    const store = new AlbumDetailStore(client as any, ms);
    await store.load("a1");

    // m1 is in album, "outside" is not
    store.pruneHidden(["m1", "outside"]);
    expect(store.album?.item_count).toBe(1);
    expect(store.album?.hidden_count).toBe(1);
    expect(store.itemIds).toEqual(["m2"]);
  });
});

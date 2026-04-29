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

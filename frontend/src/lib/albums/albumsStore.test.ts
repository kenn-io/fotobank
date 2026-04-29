import { describe, it, expect, vi } from "vitest";
import { AlbumsStore } from "./albumsStore.svelte";

function fakeClient(responses: Array<{ data?: any; error?: any }>) {
  const calls: Array<{ method: string; path: string; opts: any }> = [];
  let i = 0;
  const handler = vi.fn(async (path: string, opts: any = {}) => {
    calls.push({ method: "called", path, opts });
    return responses[i++] ?? { data: { items: [] } };
  });
  return {
    GET: handler,
    POST: handler,
    PATCH: handler,
    DELETE: handler,
    calls,
  };
}

describe("AlbumsStore.loadInitial", () => {
  it("populates albums and tracks pagination via next_offset", async () => {
    const client = fakeClient([
      {
        data: {
          items: [
            { id: "a1", name: "Italy", item_count: 12, cover: { media_id: "m1", thumb_version: 1 }, created_at: "2025-01-01", updated_at: "2025-04-01" },
            { id: "a2", name: "Family", item_count: 0, cover: null, created_at: "2024-06-01", updated_at: "2024-06-01" },
          ],
          next_offset: 100,
        },
      },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    expect(store.albums.length).toBe(2);
    expect(store.albums[0]?.name).toBe("Italy");
    expect(store.exhausted).toBe(false);
  });

  it("marks exhausted when next_offset is null", async () => {
    const client = fakeClient([{ data: { items: [], next_offset: null } }]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    expect(store.exhausted).toBe(true);
  });
});

describe("AlbumsStore.create", () => {
  it("POSTs the new album then refetches page 1", async () => {
    const client = fakeClient([
      { data: { id: "new", name: "Trip", item_count: 0, cover: null, created_at: "2026-04-28", updated_at: "2026-04-28" } },
      { data: { items: [{ id: "new", name: "Trip", item_count: 0, cover: null, created_at: "2026-04-28", updated_at: "2026-04-28" }], next_offset: null } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.create("Trip");
    expect(store.albums.length).toBe(1);
    expect(store.albums[0]?.name).toBe("Trip");
    expect(client.calls[0]?.path).toBe("/api/v1/albums"); // POST
    expect(client.calls[1]?.path).toBe("/api/v1/albums"); // GET refetch
  });

  it("rejects empty / overlong names client-side without POST", async () => {
    const client = fakeClient([]);
    const store = new AlbumsStore(client as any);
    await expect(store.create("")).rejects.toThrow();
    await expect(store.create("x".repeat(201))).rejects.toThrow();
    expect(client.calls).toHaveLength(0);
  });
});

describe("AlbumsStore.rename", () => {
  it("PATCHes and merges returned DTO into the list", async () => {
    const client = fakeClient([
      { data: { items: [{ id: "a1", name: "Old", item_count: 0, cover: null, created_at: "x", updated_at: "x" }], next_offset: null } },
      { data: { id: "a1", name: "New", item_count: 0, cover: null, created_at: "x", updated_at: "y" } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    await store.rename("a1", "New");
    expect(store.byId("a1")?.name).toBe("New");
  });
});

describe("AlbumsStore.delete", () => {
  it("DELETEs and removes from list on 204", async () => {
    const client = fakeClient([
      { data: { items: [{ id: "a1", name: "X", item_count: 0, cover: null, created_at: "x", updated_at: "x" }], next_offset: null } },
      { data: null },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    await store.delete("a1");
    expect(store.byId("a1")).toBeUndefined();
  });

  it("rethrows 409 errors so caller can surface the live-scopes toast", async () => {
    const client = fakeClient([
      { data: { items: [{ id: "a1", name: "X", item_count: 0, cover: null, created_at: "x", updated_at: "x" }], next_offset: null } },
      { error: { status: 409, message: "album has outstanding broker grants" } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    await expect(store.delete("a1")).rejects.toMatchObject({ status: 409 });
    expect(store.byId("a1")).not.toBeUndefined();
  });
});

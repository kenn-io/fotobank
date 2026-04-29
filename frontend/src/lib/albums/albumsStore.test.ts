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
            { id: "a2", name: "Family", item_count: 0, created_at: "2024-06-01", updated_at: "2024-06-01" },
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

  it("leaves exhausted=false on error so retry isn't gated as terminal", async () => {
    // A 5xx during page-1 fetch must set loadError but NOT exhausted —
    // there are still pages on the server, the user just couldn't reach
    // them. Conflating the two would hide the "Load more" affordance and
    // make the modal's auto-load effect treat retry-able state as done.
    const client = fakeClient([{ error: { status: 500, message: "boom" } }]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    expect(store.loadError).toBe(true);
    expect(store.exhausted).toBe(false);
    expect(store.albums.length).toBe(0);
  });

  it("loadMore on a partial list also leaves exhausted=false on error", async () => {
    // First page lands fine with next_offset=100; the next page errors.
    // The store must surface the error without flipping the retry-gating
    // exhausted flag — the user still has more pages to fetch.
    const client = fakeClient([
      {
        data: {
          items: [{ id: "a1", name: "A", item_count: 0, created_at: "x", updated_at: "x" }],
          next_offset: 100,
        },
      },
      { error: { status: 500, message: "boom" } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    expect(store.exhausted).toBe(false);
    expect(store.loadError).toBe(false);
    await store.loadMore();
    expect(store.loadError).toBe(true);
    expect(store.exhausted).toBe(false);
  });
});

describe("AlbumsStore.create", () => {
  it("POSTs the new album, returns the created id, then refetches page 1", async () => {
    const client = fakeClient([
      { data: { id: "new", name: "Trip", item_count: 0, created_at: "2026-04-28", updated_at: "2026-04-28" } },
      { data: { items: [{ id: "new", name: "Trip", item_count: 0, created_at: "2026-04-28", updated_at: "2026-04-28" }], next_offset: null } },
    ]);
    const store = new AlbumsStore(client as any);
    const newId = await store.create("Trip");
    // The id is captured from the POST response BEFORE the refetch, so
    // callers can select-by-id without racing the refetched list.
    expect(newId).toBe("new");
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
      { data: { items: [{ id: "a1", name: "Old", item_count: 0, created_at: "x", updated_at: "x" }], next_offset: null } },
      { data: { id: "a1", name: "New", item_count: 0, created_at: "x", updated_at: "y" } },
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
      { data: { items: [{ id: "a1", name: "X", item_count: 0, created_at: "x", updated_at: "x" }], next_offset: null } },
      { data: null },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    await store.delete("a1");
    expect(store.byId("a1")).toBeUndefined();
  });

  it("rethrows 409 errors so caller can surface the live-scopes toast", async () => {
    const client = fakeClient([
      { data: { items: [{ id: "a1", name: "X", item_count: 0, created_at: "x", updated_at: "x" }], next_offset: null } },
      { error: { status: 409, message: "album has outstanding broker grants" } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    await expect(store.delete("a1")).rejects.toMatchObject({ status: 409 });
    expect(store.byId("a1")).not.toBeUndefined();
  });
});

describe("AlbumsStore concurrent loadInitial", () => {
  it("waits for in-flight loadMore before resetting state", async () => {
    let resolveFirst!: (v: { data: any }) => void;
    const firstResponse = new Promise<{ data: any }>((res) => { resolveFirst = res; });
    const calls: string[] = [];
    let i = 0;
    const responses: Array<Promise<{ data: any }> | { data: any }> = [
      firstResponse, // page 1 — kept pending
      { data: { items: [{ id: "fresh", name: "Fresh", item_count: 0, created_at: "x", updated_at: "x" }], next_offset: null } },
    ];
    const client = {
      GET: vi.fn(async (path: string) => {
        calls.push(path);
        return responses[i++] ?? { data: { items: [] } };
      }),
      POST: vi.fn(),
      PATCH: vi.fn(),
      DELETE: vi.fn(),
    };
    const store = new AlbumsStore(client as any);
    const firstLoad = store.loadInitial();
    // Start a refresh while the first page is still pending.
    const secondLoad = store.loadInitial();
    // Resolve the first request with stale data AFTER the refresh fires.
    resolveFirst({ data: { items: [{ id: "stale", name: "Stale", item_count: 0, created_at: "x", updated_at: "x" }], next_offset: 100 } });
    await firstLoad;
    await secondLoad;
    // The fresh page should win — albums should contain the post-refresh row, not the stale row.
    expect(store.albums.find((a) => a.id === "fresh")).toBeDefined();
    expect(store.albums.find((a) => a.id === "stale")).toBeUndefined();
  });
});

describe("AlbumsStore.delete pagination correctness", () => {
  it("decrements nextOffset so the next page does not skip an album", async () => {
    const calls: any[] = [];
    let i = 0;
    const responses: Array<{ data: any }> = [
      { data: { items: [{ id: "a1", name: "A", item_count: 0, created_at: "x", updated_at: "x" }], next_offset: 100 } },
      { data: null }, // DELETE 204
    ];
    const client = {
      GET: vi.fn(async (...args: any[]) => { calls.push(["GET", ...args]); return responses[i++] ?? { data: { items: [] } }; }),
      POST: vi.fn(),
      PATCH: vi.fn(),
      DELETE: vi.fn(async (...args: any[]) => { calls.push(["DELETE", ...args]); return responses[i++] ?? { data: null }; }),
    };
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    expect((store as any).nextOffset).toBe(100);
    await store.delete("a1");
    // Loaded prefix shrunk by 1 → cursor must move back one.
    expect((store as any).nextOffset).toBe(99);
  });

  it("does not adjust nextOffset when deleting an unloaded id (no-op safety)", async () => {
    let i = 0;
    const responses: Array<{ data: any }> = [
      { data: { items: [], next_offset: 100 } },
      { data: null },
    ];
    const client = {
      GET: vi.fn(async () => responses[i++] ?? { data: { items: [] } }),
      POST: vi.fn(),
      PATCH: vi.fn(),
      DELETE: vi.fn(async () => responses[i++] ?? { data: null }),
    };
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    expect((store as any).nextOffset).toBe(100);
    await store.delete("not-in-list");
    expect((store as any).nextOffset).toBe(100); // unchanged
  });
});

describe("AlbumsStore.applyRename", () => {
  it("updates the matching album name and updated_at locally", async () => {
    const client = fakeClient([
      {
        data: {
          items: [
            { id: "a1", name: "Old", item_count: 0, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
          ],
          next_offset: null,
        },
      },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    store.applyRename("a1", { name: "New", updated_at: "2026-04-29T00:00:00Z" });
    expect(store.albums[0]?.name).toBe("New");
    expect(store.albums[0]?.updated_at).toBe("2026-04-29T00:00:00Z");
  });

  it("is a no-op when id is not in the cached list", async () => {
    const client = fakeClient([
      {
        data: {
          items: [
            { id: "a1", name: "A", item_count: 0, created_at: "x", updated_at: "x" },
          ],
          next_offset: null,
        },
      },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    store.applyRename("missing", { name: "New", updated_at: "2026-04-29T00:00:00Z" });
    expect(store.albums).toHaveLength(1);
    expect(store.albums[0]?.id).toBe("a1");
    expect(store.albums[0]?.name).toBe("A");
  });
});

describe("AlbumsStore.dropLocal", () => {
  it("removes the matching album and decrements nextOffset when > 0", async () => {
    const client = fakeClient([
      {
        data: {
          items: [
            { id: "a1", name: "A", item_count: 0, created_at: "x", updated_at: "x" },
            { id: "a2", name: "B", item_count: 0, created_at: "x", updated_at: "x" },
          ],
          next_offset: 5,
        },
      },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    store.dropLocal("a1");
    expect(store.albums).toHaveLength(1);
    expect(store.albums[0]?.id).toBe("a2");
    expect((store as any).nextOffset).toBe(4);
  });

  it("is a no-op when id is not in the cached list", async () => {
    const client = fakeClient([
      {
        data: {
          items: [
            { id: "a1", name: "A", item_count: 0, created_at: "x", updated_at: "x" },
          ],
          next_offset: 100,
        },
      },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    store.dropLocal("missing");
    expect(store.albums).toHaveLength(1);
    expect((store as any).nextOffset).toBe(100);
  });
});

describe("AlbumsStore.loadInitial parses hidden_count", () => {
  it("surfaces hidden_count from the API response on each album", async () => {
    const client = fakeClient([
      {
        data: {
          items: [
            { id: "a1", name: "Italy", item_count: 95, hidden_count: 5, created_at: "x", updated_at: "x" },
            { id: "a2", name: "Family", item_count: 0, hidden_count: 0, created_at: "x", updated_at: "x" },
          ],
          next_offset: null,
        },
      },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    expect(store.albums[0]?.hidden_count).toBe(5);
    expect(store.albums[1]?.hidden_count).toBe(0);
  });
});

describe("AlbumsStore.markStale / refreshIfStale", () => {
  it("refreshIfStale is a no-op when not stale", async () => {
    const client = fakeClient([
      { data: { items: [{ id: "a1", name: "A", item_count: 0, created_at: "x", updated_at: "x" }], next_offset: null } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    const callsBefore = client.calls.length;
    await store.refreshIfStale();
    expect(client.calls.length).toBe(callsBefore);
  });

  it("markStale causes refreshIfStale to refetch", async () => {
    const client = fakeClient([
      { data: { items: [{ id: "a1", name: "A", item_count: 0, created_at: "x", updated_at: "x" }], next_offset: null } },
      { data: { items: [{ id: "a1", name: "A-fresh", item_count: 1, created_at: "x", updated_at: "x" }], next_offset: null } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    store.markStale();
    await store.refreshIfStale();
    expect(store.albums[0]?.name).toBe("A-fresh");
  });

  it("refreshIfStale clears the stale flag after refetch", async () => {
    const client = fakeClient([
      { data: { items: [], next_offset: null } },
      { data: { items: [], next_offset: null } },
    ]);
    const store = new AlbumsStore(client as any);
    await store.loadInitial();
    store.markStale();
    await store.refreshIfStale();
    const callsAfterFirstRefresh = client.calls.length;
    // second refreshIfStale should be a no-op — stale was cleared
    await store.refreshIfStale();
    expect(client.calls.length).toBe(callsAfterFirstRefresh);
  });
});

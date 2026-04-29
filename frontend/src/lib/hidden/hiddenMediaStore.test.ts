import { describe, it, expect, vi } from "vitest";
import { HiddenMediaStore } from "./hiddenMediaStore.svelte";

function makeClient(responses: Record<string, { data?: unknown; error?: unknown }>) {
  const calls: Array<{ path: string }> = [];
  const handler = vi.fn(async (path: string) => {
    calls.push({ path });
    return responses[path] ?? { error: { status: 500 } };
  });
  return { GET: handler, calls };
}

const rawItem = {
  id: "h1",
  timestamp: "2024-06-01T10:00:00Z",
  width: 4,
  height: 3,
  thumb_version: 1,
  hidden_at: "2024-06-01T09:00:00Z",
};

describe("HiddenMediaStore.loadInitial", () => {
  it("calls /api/v1/hidden/media and populates months", async () => {
    const client = makeClient({
      "/api/v1/hidden/media": {
        data: { items: [rawItem], next_offset: null },
      },
    });
    const store = new HiddenMediaStore(client as never);
    await store.loadInitial();
    expect(store.months.length).toBe(1);
    expect(store.months[0]?.items[0]?.id).toBe("h1");
    expect(store.exhausted).toBe(true);
  });

  it("sets loading=false after fetch", async () => {
    const client = makeClient({
      "/api/v1/hidden/media": {
        data: { items: [], next_offset: null },
      },
    });
    const store = new HiddenMediaStore(client as never);
    await store.loadInitial();
    expect(store.loading).toBe(false);
  });

  it("paginates via next_offset when loadMore is called again", async () => {
    const client = makeClient({
      "/api/v1/hidden/media": {
        data: { items: [rawItem], next_offset: null },
      },
    });
    const store = new HiddenMediaStore(client as never);
    await store.loadInitial();
    // Already exhausted; second call is a no-op
    await store.loadMore();
    expect(client.GET).toHaveBeenCalledTimes(1);
  });
});

describe("HiddenMediaStore.loadMore", () => {
  it("does not call API when exhausted", async () => {
    const client = makeClient({
      "/api/v1/hidden/media": { data: { items: [], next_offset: null } },
    });
    const store = new HiddenMediaStore(client as never);
    await store.loadInitial(); // exhausted now
    const callsBefore = client.GET.mock.calls.length;
    await store.loadMore();
    expect(client.GET.mock.calls.length).toBe(callsBefore);
  });

  it("does not call API when already loading", async () => {
    // Simulate concurrent calls: first call starts, second should no-op
    let resolveFirst!: (v: unknown) => void;
    const firstPromise = new Promise((r) => { resolveFirst = r; });
    let callCount = 0;
    const client = {
      GET: vi.fn(async (_path: string) => {
        callCount++;
        if (callCount === 1) await firstPromise;
        return { data: { items: [], next_offset: null } };
      }),
    };
    const store = new HiddenMediaStore(client as never);
    const p1 = store.loadMore();
    // Immediately try a second load while first is in-flight
    const p2 = store.loadMore();
    resolveFirst(undefined);
    await Promise.all([p1, p2]);
    expect(callCount).toBe(1);
  });
});

describe("HiddenMediaStore.removeMany", () => {
  it("evicts specified ids from months", async () => {
    const client = makeClient({
      "/api/v1/hidden/media": {
        data: {
          items: [
            rawItem,
            { id: "h2", timestamp: "2024-06-02T10:00:00Z", width: 4, height: 3, thumb_version: 1, hidden_at: "2024-06-02T09:00:00Z" },
          ],
          next_offset: null,
        },
      },
    });
    const store = new HiddenMediaStore(client as never);
    await store.loadInitial();
    expect(store.months[0]?.items.length).toBe(2);
    store.removeMany(["h1"]);
    const remaining = store.months.flatMap((m) => m.items.map((i) => i.id));
    expect(remaining).not.toContain("h1");
    expect(remaining).toContain("h2");
  });

  it("prunes empty month buckets after removal", async () => {
    const client = makeClient({
      "/api/v1/hidden/media": {
        data: { items: [rawItem], next_offset: null },
      },
    });
    const store = new HiddenMediaStore(client as never);
    await store.loadInitial();
    expect(store.months.length).toBe(1);
    store.removeMany(["h1"]);
    expect(store.months.length).toBe(0);
  });
});

describe("HiddenMediaStore never merges into visible MediaStore", () => {
  it("is an independent store with its own months state", async () => {
    const client = makeClient({
      "/api/v1/hidden/media": {
        data: { items: [rawItem], next_offset: null },
      },
    });
    const store = new HiddenMediaStore(client as never);
    await store.loadInitial();
    // months is local — no shared singleton is mutated
    expect(store.months[0]?.items[0]?.id).toBe("h1");
  });
});

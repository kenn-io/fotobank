import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { SharesStore } from "./sharesStore.svelte";

function fakeClient(responses: Array<any>) {
  let i = 0;
  const calls: Array<{ method: string; path: string; opts: any }> = [];
  const handler = vi.fn(async (path: string, opts: any = {}) => {
    calls.push({ method: "h", path, opts });
    return responses[i++] ?? { data: null };
  });
  return { GET: handler, POST: handler, DELETE: handler, calls };
}

const baseRow = {
  uuid: "s1",
  target_type: "media_set" as const,
  target_album_id: null,
  target_summary: { label: "1 photo", item_count: 1 },
  grantee: { hub: "h", user_id: "b" },
  grantee_handle: "Bob",
  allow_download: false,
  label: "",
  created_at: "2026-04-28T00:00:00Z",
  expires_at: null,
  revoked_at: null,
  broker_status: "active" as const,
  broker_attempts: 0,
  broker_last_error: "",
};

describe("SharesStore.loadInitial", () => {
  it("fetches and stores list rows", async () => {
    const client = fakeClient([
      { data: { items: [baseRow], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    expect(store.scopes).toHaveLength(1);
    expect(store.scopes[0]?.uuid).toBe("s1");
    expect(store.exhausted).toBe(true);
  });

  it("sets loadError and clears loading when GET returns res.error", async () => {
    const client = fakeClient([
      { error: { status: 500, message: "boom" } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    expect(store.scopes).toHaveLength(0);
    expect(store.loading).toBe(false);
    expect(store.loadError).toBe(true);
    // Marked exhausted so auto-retry effects don't loop on persistent failure.
    expect(store.exhausted).toBe(true);
  });
});

describe("SharesStore.create", () => {
  it("POSTs and refetches the list", async () => {
    const client = fakeClient([
      { data: { ...baseRow, uuid: "new" } },
      { data: { items: [{ ...baseRow, uuid: "new" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.create({
      target_type: "media_set",
      media_ids: ["m1"],
      grantee: { hub: "h", user_id: "b" },
      label: "",
      allow_download: false,
    });
    expect(store.scopes).toHaveLength(1);
    expect(store.scopes[0]?.uuid).toBe("new");
  });

  it("rethrows when POST errors", async () => {
    const client = fakeClient([
      { error: { status: 400, message: "bad request" } },
    ]);
    const store = new SharesStore(client as any);
    await expect(
      store.create({
        target_type: "media_set",
        media_ids: ["m1"],
        grantee: { hub: "h", user_id: "b" },
        label: "",
        allow_download: false,
      }),
    ).rejects.toMatchObject({ status: 400 });
  });
});

describe("SharesStore.revoke / retry", () => {
  it("revoke posts then refetches", async () => {
    const client = fakeClient([
      { data: { items: [baseRow], next_offset: null } },
      { data: null },
      { data: { items: [{ ...baseRow, broker_status: "revoking" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    await store.revoke("s1");
    expect(store.scopes[0]?.broker_status).toBe("revoking");
  });

  it("retry posts then refetches", async () => {
    const client = fakeClient([
      { data: { items: [{ ...baseRow, broker_status: "failed" }], next_offset: null } },
      { data: null },
      { data: { items: [{ ...baseRow, broker_status: "pending" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    await store.retry("s1");
    expect(store.scopes[0]?.broker_status).toBe("pending");
  });

  it("revoke rethrows when POST errors", async () => {
    const client = fakeClient([
      { data: { items: [baseRow], next_offset: null } },
      { error: { status: 500, message: "boom" } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    await expect(store.revoke("s1")).rejects.toMatchObject({ status: 500 });
  });

  it("retry rethrows when POST errors", async () => {
    const client = fakeClient([
      { data: { items: [{ ...baseRow, broker_status: "failed" }], next_offset: null } },
      { error: { status: 500, message: "boom" } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    await expect(store.retry("s1")).rejects.toMatchObject({ status: 500 });
  });
});

describe("SharesStore polling", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("starts polling when a row is pending", async () => {
    const client = fakeClient([
      { data: { items: [{ ...baseRow, broker_status: "pending" }], next_offset: null } },
      { data: { items: [{ ...baseRow, broker_status: "pending" }], next_offset: null } },
      { data: { items: [{ ...baseRow, broker_status: "active" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    // First poll tick
    await vi.advanceTimersByTimeAsync(5000);
    expect(store.scopes[0]?.broker_status).toBe("pending");
    // Second tick — broker activates the share
    await vi.advanceTimersByTimeAsync(5000);
    expect(store.scopes[0]?.broker_status).toBe("active");
    // Third tick — should NOT happen (poll auto-stops on settled)
    await vi.advanceTimersByTimeAsync(5000);
    expect(client.calls.length).toBe(3);
  });

  it("stops polling once all rows are settled", async () => {
    const client = fakeClient([
      { data: { items: [{ ...baseRow, broker_status: "active" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    await vi.advanceTimersByTimeAsync(20000);
    expect(client.calls.length).toBe(1);
  });

  it("uses include_settled=true regardless of showRevoked=false", async () => {
    const client = fakeClient([
      // loadInitial — pending so polling starts
      { data: { items: [{ ...baseRow, broker_status: "pending" }], next_offset: null } },
      // first poll tick — still pending so polling continues
      { data: { items: [{ ...baseRow, broker_status: "pending" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    expect(store.showRevoked).toBe(false);
    await store.loadInitial();
    await vi.advanceTimersByTimeAsync(5000);
    // calls[0] is loadInitial's GET, calls[1] is the first poll tick.
    const pollCall = client.calls[1];
    expect(pollCall?.opts.params.query.include_settled).toBe(true);
  });
});

describe("SharesStore.getDetail / getPreview", () => {
  it("caches detail after first fetch", async () => {
    const client = fakeClient([
      { data: { ...baseRow, media_ids: ["m1"] } },
    ]);
    const store = new SharesStore(client as any);
    const det1 = await store.getDetail("s1");
    const det2 = await store.getDetail("s1");
    expect(client.calls.length).toBe(1);
    expect(det1).toBe(det2);
  });

  it("getDetail returns null on error and does not poison cache", async () => {
    const detail = { ...baseRow, media_ids: ["m1"] };
    const client = fakeClient([
      { error: { status: 500, message: "boom" } },
      { data: detail },
    ]);
    const store = new SharesStore(client as any);
    const first = await store.getDetail("s1");
    expect(first).toBeNull();
    // Cache should not store the error — second call hits the network and succeeds.
    const second = await store.getDetail("s1");
    expect(second).toEqual(detail);
    expect(client.calls.length).toBe(2);
  });

  it("getPreview returns null on error and does not poison cache", async () => {
    const preview = { ok: true };
    const client = fakeClient([
      { error: { status: 502, message: "broker unavailable" } },
      { data: preview },
    ]);
    const store = new SharesStore(client as any);
    const first = await store.getPreview("s1");
    expect(first).toBeNull();
    const second = await store.getPreview("s1");
    expect(second).toEqual(preview);
    expect(client.calls.length).toBe(2);
  });
});

describe("SharesStore.setAlbumIDFilter", () => {
  it("issues a fetch with album_id query param", async () => {
    const client = fakeClient([
      // loadInitial baseline (no filter)
      { data: { items: [], next_offset: null } },
      // refetch after filter change
      { data: { items: [{ ...baseRow, target_album_id: "alb-1" }], next_offset: null } },
    ]);
    const store = new SharesStore(client as any);
    await store.loadInitial();
    await store.setAlbumIDFilter("alb-1");
    expect(client.calls[1]?.opts.params.query.album_id).toBe("alb-1");
    expect(store.scopes[0]?.target_album_id).toBe("alb-1");
  });
});

describe("SharesStore concurrent loadInitial", () => {
  it("does not produce duplicate or stale rows", async () => {
    let resolveFirst!: (v: { data: any }) => void;
    const firstResponse = new Promise<{ data: any }>((res) => { resolveFirst = res; });
    let i = 0;
    const responses: Array<Promise<{ data: any }> | { data: any }> = [
      firstResponse, // first loadInitial — kept pending
      { data: { items: [{ ...baseRow, uuid: "fresh" }], next_offset: null } },
    ];
    const client = {
      GET: vi.fn(async () => {
        return responses[i++] ?? { data: { items: [] } };
      }),
      POST: vi.fn(),
      DELETE: vi.fn(),
    };
    const store = new SharesStore(client as any);
    const firstLoad = store.loadInitial();
    // Start a refresh while the first page is still pending.
    const secondLoad = store.loadInitial();
    // Resolve the stale request AFTER the refresh fires.
    resolveFirst({ data: { items: [{ ...baseRow, uuid: "stale" }], next_offset: null } });
    await firstLoad;
    await secondLoad;
    // Only the fresh response should be reflected; no duplicates and no stale row.
    expect(store.scopes.find((s) => s.uuid === "fresh")).toBeDefined();
    expect(store.scopes.find((s) => s.uuid === "stale")).toBeUndefined();
    expect(store.scopes.length).toBe(1);
  });
});

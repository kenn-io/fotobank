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
});

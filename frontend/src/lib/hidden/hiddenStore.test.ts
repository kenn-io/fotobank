import { describe, it, expect, vi } from "vitest";
import { HiddenStore } from "./hiddenStore.svelte";

function makeClient(responses: Record<string, { data?: unknown; error?: unknown }>) {
  const calls: Array<{ method: string; path: string; body?: unknown }> = [];
  const handler = vi.fn(async (path: string, opts: Record<string, unknown> = {}) => {
    calls.push({ method: "called", path, body: (opts as { body?: unknown }).body });
    return responses[path] ?? { error: { status: 500 } };
  });
  return { GET: handler, POST: handler, calls };
}

describe("HiddenStore.refresh", () => {
  it("updates configured, unlocked, expiresAt from state endpoint", async () => {
    const client = makeClient({
      "/api/v1/auth/hidden/state": {
        data: { configured: true, unlocked: true, expires_at: "2099-01-01T00:00:00Z" },
      },
    });
    const store = new HiddenStore(client as never);
    await store.refresh();
    expect(store.configured).toBe(true);
    expect(store.unlocked).toBe(true);
    expect(store.expiresAt).toBe("2099-01-01T00:00:00Z");
    expect(store.error).toBeNull();
  });

  it("sets configured=false, unlocked=false when not configured", async () => {
    const client = makeClient({
      "/api/v1/auth/hidden/state": {
        data: { configured: false, unlocked: false },
      },
    });
    const store = new HiddenStore(client as never);
    await store.refresh();
    expect(store.configured).toBe(false);
    expect(store.unlocked).toBe(false);
    expect(store.expiresAt).toBeNull();
  });

  it("silently ignores network errors on refresh (non-throwing)", async () => {
    const client = makeClient({
      "/api/v1/auth/hidden/state": { error: { status: 500 } },
    });
    const store = new HiddenStore(client as never);
    // Should not throw
    await expect(store.refresh()).resolves.toBeUndefined();
  });
});

describe("HiddenStore.unlock", () => {
  it("calls unlock endpoint and refreshes state on 204", async () => {
    const client = makeClient({
      "/api/v1/auth/hidden/unlock": { data: undefined }, // 204 no body
      "/api/v1/auth/hidden/state": {
        data: { configured: true, unlocked: true, expires_at: "2099-01-01T00:00:00Z" },
      },
    });
    const store = new HiddenStore(client as never);
    await store.unlock("secret");
    expect(store.unlocked).toBe(true);
    expect(store.error).toBeNull();
  });

  it("sets error.kind='wrong_passcode' on 403", async () => {
    const client = makeClient({
      "/api/v1/auth/hidden/unlock": { error: { status: 403 } },
      "/api/v1/auth/hidden/state": {
        data: { configured: true, unlocked: false },
      },
    });
    const store = new HiddenStore(client as never);
    await expect(store.unlock("wrong")).rejects.toBeDefined();
    expect(store.error?.kind).toBe("wrong_passcode");
  });

  it("sets error.kind='locked_out' with retryAfterSeconds on 429", async () => {
    const client = makeClient({
      "/api/v1/auth/hidden/unlock": {
        error: { status: 429, headers: { "retry-after": "120" } },
      },
    });
    const store = new HiddenStore(client as never);
    await expect(store.unlock("wrong")).rejects.toBeDefined();
    expect(store.error?.kind).toBe("locked_out");
    if (store.error?.kind === "locked_out") {
      expect(store.error.retryAfterSeconds).toBeGreaterThan(0);
    }
  });

  it("sets error.kind='invalid_input' on 400", async () => {
    const client = makeClient({
      "/api/v1/auth/hidden/unlock": { error: { status: 400 } },
    });
    const store = new HiddenStore(client as never);
    await expect(store.unlock("")).rejects.toBeDefined();
    expect(store.error?.kind).toBe("invalid_input");
  });

  it("sets error.kind='identity_required' on 401", async () => {
    const client = makeClient({
      "/api/v1/auth/hidden/unlock": { error: { status: 401 } },
    });
    const store = new HiddenStore(client as never);
    await expect(store.unlock("x")).rejects.toBeDefined();
    expect(store.error?.kind).toBe("identity_required");
  });
});

describe("HiddenStore.lock", () => {
  it("optimistically clears state and calls lock endpoint, then refreshes", async () => {
    const client = makeClient({
      "/api/v1/auth/hidden/lock": { data: undefined },
      "/api/v1/auth/hidden/state": {
        data: { configured: true, unlocked: false },
      },
    });
    const store = new HiddenStore(client as never);
    // Manually set unlocked state
    store.configured = true;
    store.unlocked = true;
    store.expiresAt = "2099-01-01T00:00:00Z";

    await store.lock();
    expect(store.unlocked).toBe(false);
    expect(store.expiresAt).toBeNull();
  });

  it("keepalive=true optimistically clears without awaiting refresh", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(null, { status: 204 }),
    );
    const client = makeClient({});
    const store = new HiddenStore(client as never);
    store.unlocked = true;
    store.expiresAt = "2099-01-01T00:00:00Z";

    // Should complete synchronously (no refresh awaited)
    await store.lock({ keepalive: true });
    expect(store.unlocked).toBe(false);
    expect(store.expiresAt).toBeNull();
    // Does not call the api client's POST
    expect(client.POST).not.toHaveBeenCalled();
    // Uses fetch with keepalive
    expect(fetchSpy).toHaveBeenCalledWith(
      "/api/v1/auth/hidden/lock",
      expect.objectContaining({ method: "POST", keepalive: true }),
    );
    fetchSpy.mockRestore();
  });
});

describe("HiddenStore.hide", () => {
  it("calls hide bulk endpoint and returns result", async () => {
    const client = makeClient({
      "/api/v1/media/hidden:bulk": {
        data: { succeeded: ["id1", "id2"], failed: null },
      },
    });
    const store = new HiddenStore(client as never);
    const result = await store.hide(["id1", "id2"]);
    expect(result.succeeded).toEqual(["id1", "id2"]);
    expect(result.failed).toBeNull();
  });

  it("throws on error response", async () => {
    const client = makeClient({
      "/api/v1/media/hidden:bulk": { error: { status: 409 } },
    });
    const store = new HiddenStore(client as never);
    await expect(store.hide(["id1"])).rejects.toBeDefined();
  });
});

describe("HiddenStore.unhide", () => {
  it("calls unhide bulk endpoint and returns result", async () => {
    const client = makeClient({
      "/api/v1/media/unhide:bulk": {
        data: { succeeded: ["id3"], failed: null },
      },
    });
    const store = new HiddenStore(client as never);
    const result = await store.unhide(["id3"]);
    expect(result.succeeded).toEqual(["id3"]);
  });

  it("refreshes state and throws on 403 (cookie expired mid-action)", async () => {
    const client = makeClient({
      "/api/v1/media/unhide:bulk": { error: { status: 403 } },
      "/api/v1/auth/hidden/state": {
        data: { configured: true, unlocked: false },
      },
    });
    const store = new HiddenStore(client as never);
    store.unlocked = true;
    await expect(store.unhide(["id1"])).rejects.toBeDefined();
    // After 403 the store should have refreshed state
    expect(store.unlocked).toBe(false);
  });
});

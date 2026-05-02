import { describe, it, expect } from "vitest";
import { AppConfigStore } from "./appConfig.svelte";

type Resp = { data?: unknown; error?: unknown };

function makeClient(responses: Record<string, Resp>) {
  const calls: Array<{ path: string }> = [];
  const handler = async (path: string, _opts: Record<string, unknown> = {}) => {
    calls.push({ path });
    return responses[path] ?? { error: { status: 500 } };
  };
  return { GET: handler, calls };
}

describe("AppConfigStore", () => {
  it("starts with sharingEnabled=false and ready=false", () => {
    const client = makeClient({});
    const s = new AppConfigStore(client as never);
    expect(s.sharingEnabled).toBe(false);
    expect(s.ready).toBe(false);
  });

  it("flips sharingEnabled when /me reports true", async () => {
    const client = makeClient({
      "/api/v1/me": {
        data: {
          principal: { hub: "h", user_id: "u", handle: "" },
          scopes: [],
          features: { sharing_enabled: true },
        },
      },
    });
    const s = new AppConfigStore(client as never);
    await s.load();
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(true);
    expect(client.calls).toEqual([{ path: "/api/v1/me" }]);
  });

  it("treats a /me HTTP failure as resolved-disabled (so /shares redirect can fire)", async () => {
    const client = makeClient({ "/api/v1/me": { error: { status: 500 } } });
    const s = new AppConfigStore(client as never);
    await s.load();
    // ready flips to true on BOTH success and failure so route guards
    // (e.g. the /shares redirect in D5) fire even when /me is down.
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
  });

  it("treats a thrown network error as resolved-disabled", async () => {
    const client = {
      GET: async () => {
        throw new Error("network");
      },
    };
    const s = new AppConfigStore(client as never);
    await s.load();
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
  });

  it("treats a non-true sharing_enabled value as false", async () => {
    // Server returned ok but sent a non-boolean for sharing_enabled.
    // The strict ===true compare coerces this to false defensively.
    const client = makeClient({
      "/api/v1/me": {
        data: {
          principal: { hub: "h", user_id: "u", handle: "" },
          scopes: [],
          features: { sharing_enabled: "yes" }, // not literal true
        },
      },
    });
    const s = new AppConfigStore(client as never);
    await s.load();
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
  });
});

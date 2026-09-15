import { describe, it, expect } from "vitest";
import { AppConfigStore } from "./appConfig.svelte";

type Resp = { data?: unknown; error?: unknown };

function makeClient(responses: Record<string, Resp>) {
  const calls: Array<{ path: string }> = [];
  const handler = async (path: string, _opts: Record<string, unknown> = {}) => {
    calls.push({ path });
    return responses[path] ?? { error: { status: 500 } };
  };
  return { GET: handler, calls ,
me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); }
};
}

describe("AppConfigStore", () => {
  it("starts with sharingEnabled=false, principal=null, ready=false", () => {
    const client = makeClient({});
    const s = new AppConfigStore(client as never);
    expect(s.sharingEnabled).toBe(false);
    expect(s.adminSettingsEnabled).toBe(false);
    expect(s.principal).toBeNull();
    expect(s.ready).toBe(false);
  });

  it("captures principal and feature flags when /me reports true", async () => {
    const client = makeClient({
      "/api/v1/me": {
        data: {
          principal: { hub: "dev-local", user_id: "owner", handle: "owner" },
          scopes: [],
          features: { sharing_enabled: true, admin_settings_enabled: true },
        },
      },
    });
    const s = new AppConfigStore(client as never);
    await s.load();
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(true);
    expect(s.adminSettingsEnabled).toBe(true);
    expect(s.principal).toEqual({
      hub: "dev-local",
      userId: "owner",
      handle: "owner",
    });
    expect(client.calls).toEqual([{ path: "/api/v1/me" }]);
  });

  it("treats a /me HTTP failure as resolved-disabled with no principal", async () => {
    const client = makeClient({ "/api/v1/me": { error: { status: 500 } } });
    const s = new AppConfigStore(client as never);
    await s.load();
    // ready flips to true on BOTH success and failure so route guards
    // (e.g. the /shares redirect in D5) fire even when /me is down.
    // principal stays null so AppHeader renders "—" rather than stale
    // identity from a previous load.
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
    expect(s.adminSettingsEnabled).toBe(false);
    expect(s.principal).toBeNull();
  });

  it("treats a thrown network error as resolved-disabled with no principal", async () => {
    const client = {
      GET: async () => {
        throw new Error("network");
      },

me(options?: any) { return (this as any).GET("/api/v1/me", { ...options }); }
};
    const s = new AppConfigStore(client as never);
    await s.load();
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
    expect(s.adminSettingsEnabled).toBe(false);
    expect(s.principal).toBeNull();
  });

  it("falls back handle → user_id when /me returns an empty handle string", async () => {
    // The schema marks handle optional (PrincipalStruct.handle?: string).
    // Some configs set it to "" rather than omitting it entirely; that's
    // the same intent — no friendly name available — and AppHeader would
    // render `dev-local: ` with a trailing space if the store let "" through.
    const client = makeClient({
      "/api/v1/me": {
        data: {
          principal: { hub: "dev-local", user_id: "owner", handle: "" },
          scopes: [],
          features: { sharing_enabled: false, admin_settings_enabled: false },
        },
      },
    });
    const s = new AppConfigStore(client as never);
    await s.load();
    expect(s.principal).toEqual({
      hub: "dev-local",
      userId: "owner",
      handle: "owner",
    });
  });

  it("treats a non-true sharing_enabled value as false but still captures principal", async () => {
    // Server returned ok but sent a non-boolean for sharing_enabled.
    // The strict ===true compare coerces this to false defensively.
    // Principal capture is independent so the header still renders the
    // real owner even when the features block is malformed.
    const client = makeClient({
      "/api/v1/me": {
        data: {
          principal: { hub: "h", user_id: "u", handle: "owner" },
          scopes: [],
          features: { sharing_enabled: "yes", admin_settings_enabled: "yes" }, // not literal true
        },
      },
    });
    const s = new AppConfigStore(client as never);
    await s.load();
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
    expect(s.adminSettingsEnabled).toBe(false);
    expect(s.principal).toEqual({ hub: "h", userId: "u", handle: "owner" });
  });
});

import { describe, expect, it, vi, beforeEach } from "vitest";
import { AppConfigStore } from "./appConfig.svelte";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("AppConfigStore", () => {
  it("starts with sharingEnabled=false and ready=false", () => {
    const s = new AppConfigStore();
    expect(s.sharingEnabled).toBe(false);
    expect(s.ready).toBe(false);
  });

  it("flips sharingEnabled when /me reports true", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        principal: { hub: "h", user_id: "u" },
        scopes: [],
        features: { sharing_enabled: true },
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    const s = new AppConfigStore();
    await s.load();

    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/me");
  });

  it("treats a /me failure as resolved-disabled (so /shares redirect can fire)", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 500 }));
    const s = new AppConfigStore();
    await s.load();
    // ready flips to true on BOTH success and failure so route guards
    // (e.g. the /shares redirect in D5) fire even when /me is down.
    // The default sharingEnabled=false means "treat failure as disabled".
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
  });

  it("treats a network error as resolved-disabled", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("network")));
    const s = new AppConfigStore();
    await s.load();
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
  });

  it("treats a missing features.sharing_enabled as false", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ principal: { hub: "h", user_id: "u" }, scopes: [] }),
    }));
    const s = new AppConfigStore();
    await s.load();
    expect(s.ready).toBe(true);
    expect(s.sharingEnabled).toBe(false);
  });
});

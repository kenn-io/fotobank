import { describe, it, expect, vi } from "vitest";
import { DensityStore, ROW_HEIGHTS } from "./densityStore.svelte";

describe("DensityStore", () => {
  it("defaults to comfortable", () => {
    const store = new DensityStore({ GET: vi.fn(), PUT: vi.fn() ,
getUserSetting(key?: any, options?: any) { return (this as any).GET("/api/v1/settings/user/{key}", { params: { path: { key } }, ...options }); },
putUserSetting(key?: any, userSettingPutInputBody?: any, options?: any) { return (this as any).PUT("/api/v1/settings/user/{key}", { params: { path: { key } }, body: userSettingPutInputBody, ...options }); }
} as never, "library");
    expect(store.preset).toBe("comfortable");
    expect(store.targetRowHeight).toBe(ROW_HEIGHTS.comfortable);
  });

  it("persists per-context key", async () => {
    const PUT = vi.fn().mockResolvedValue({ error: undefined });
    const store = new DensityStore({ GET: vi.fn(), PUT ,
getUserSetting(key?: any, options?: any) { return (this as any).GET("/api/v1/settings/user/{key}", { params: { path: { key } }, ...options }); },
putUserSetting(key?: any, userSettingPutInputBody?: any, options?: any) { return (this as any).PUT("/api/v1/settings/user/{key}", { params: { path: { key } }, body: userSettingPutInputBody, ...options }); }
} as never, "library");
    await store.set("compact");
    expect(PUT).toHaveBeenCalledWith(
      "/api/v1/settings/user/{key}",
      expect.objectContaining({ params: { path: { key: "density.library" } } }),
    );
    expect(store.preset).toBe("compact");
  });

  it("set() during pending load() wins over the late server response", async () => {
    // Routes fire load() at mount; if the user clicks before the GET
    // resolves, the response must not overwrite their newer choice.
    let resolveGet: (v: { data: { value: string }; error: undefined }) => void;
    const GET = vi.fn().mockImplementation(
      () => new Promise((res) => { resolveGet = res; }),
    );
    const PUT = vi.fn().mockResolvedValue({ error: undefined });
    const store = new DensityStore({ GET, PUT ,
getUserSetting(key?: any, options?: any) { return (this as any).GET("/api/v1/settings/user/{key}", { params: { path: { key } }, ...options }); },
putUserSetting(key?: any, userSettingPutInputBody?: any, options?: any) { return (this as any).PUT("/api/v1/settings/user/{key}", { params: { path: { key } }, body: userSettingPutInputBody, ...options }); }
} as never, "library");
    const loading = store.load();
    await store.set("compact"); // user clicks before GET resolves
    expect(store.preset).toBe("compact");
    resolveGet!({ data: { value: JSON.stringify("large") }, error: undefined });
    await loading;
    expect(store.preset).toBe("compact");
  });

  it("nudge bumps within bounds", async () => {
    // nudge() calls set() (which awaits PUT) but doesn't await — so we
    // mock PUT to resolve to satisfy the unhandled-rejection check.
    const PUT = vi.fn().mockResolvedValue({ error: undefined });
    const store = new DensityStore({ GET: vi.fn(), PUT ,
getUserSetting(key?: any, options?: any) { return (this as any).GET("/api/v1/settings/user/{key}", { params: { path: { key } }, ...options }); },
putUserSetting(key?: any, userSettingPutInputBody?: any, options?: any) { return (this as any).PUT("/api/v1/settings/user/{key}", { params: { path: { key } }, body: userSettingPutInputBody, ...options }); }
} as never, "library");
    store.preset = "compact";
    store.nudge(1);
    expect(store.preset).toBe("comfortable");
    store.nudge(1);
    expect(store.preset).toBe("large");
    store.nudge(1); // already at max, stays
    expect(store.preset).toBe("large");
  });
});

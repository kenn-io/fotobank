import { describe, it, expect, vi } from "vitest";
import { ThemeStore } from "./themeStore.svelte";

describe("ThemeStore", () => {
  it("defaults to system when no override is loaded", () => {
    const store = new ThemeStore({
      GET: vi.fn().mockResolvedValue({ data: undefined, error: { status: 404 } }),
      PUT: vi.fn(),
    } as never);
    expect(store.theme).toBe("system");
  });

  it("applies override after load", async () => {
    const GET = vi.fn().mockResolvedValue({ data: { value: '"dark"' }, error: undefined });
    const store = new ThemeStore({ GET, PUT: vi.fn() } as never);
    await store.load();
    expect(store.theme).toBe("dark");
    expect(store.loaded).toBe(true);
  });

  it("persists override on set", async () => {
    const PUT = vi.fn().mockResolvedValue({ error: undefined });
    const store = new ThemeStore({ GET: vi.fn(), PUT } as never);
    await store.set("light");
    expect(PUT).toHaveBeenCalledWith(
      "/api/v1/settings/user/{key}",
      expect.objectContaining({
        params: { path: { key: "theme" } },
        body: { value: '"light"' },
      }),
    );
    expect(store.theme).toBe("light");
  });

  it("ignores malformed stored values", async () => {
    const GET = vi.fn().mockResolvedValue({ data: { value: "not-json" }, error: undefined });
    const store = new ThemeStore({ GET, PUT: vi.fn() } as never);
    await store.load();
    expect(store.theme).toBe("system"); // unchanged
    expect(store.loaded).toBe(true);
  });
});

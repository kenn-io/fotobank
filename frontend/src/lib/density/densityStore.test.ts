import { describe, it, expect, vi } from "vitest";
import { DensityStore, ROW_HEIGHTS } from "./densityStore.svelte";

describe("DensityStore", () => {
  it("defaults to comfortable", () => {
    const store = new DensityStore({ GET: vi.fn(), PUT: vi.fn() } as never, "library");
    expect(store.preset).toBe("comfortable");
    expect(store.targetRowHeight).toBe(ROW_HEIGHTS.comfortable);
  });

  it("persists per-context key", async () => {
    const PUT = vi.fn().mockResolvedValue({ error: undefined });
    const store = new DensityStore({ GET: vi.fn(), PUT } as never, "library");
    await store.set("compact");
    expect(PUT).toHaveBeenCalledWith(
      "/api/v1/settings/user/{key}",
      expect.objectContaining({ params: { path: { key: "density.library" } } }),
    );
    expect(store.preset).toBe("compact");
  });

  it("nudge bumps within bounds", async () => {
    // nudge() calls set() (which awaits PUT) but doesn't await — so we
    // mock PUT to resolve to satisfy the unhandled-rejection check.
    const PUT = vi.fn().mockResolvedValue({ error: undefined });
    const store = new DensityStore({ GET: vi.fn(), PUT } as never, "library");
    store.preset = "compact";
    store.nudge(1);
    expect(store.preset).toBe("comfortable");
    store.nudge(1);
    expect(store.preset).toBe("large");
    store.nudge(1); // already at max, stays
    expect(store.preset).toBe("large");
  });
});

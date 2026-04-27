import { describe, it, expect, vi } from "vitest";
import { MediaStore, monthKey } from "./mediaStore.svelte";

describe("monthKey", () => {
  it("buckets a date into YYYY-MM", () => {
    expect(monthKey(new Date("2026-04-18T12:00:00Z"))).toBe("2026-04");
    expect(monthKey(new Date("2026-12-31T23:59:00Z"))).toBe("2026-12");
  });
});

describe("MediaStore", () => {
  it("groups loaded media by month, descending", async () => {
    const fakeClient = {
      GET: vi.fn().mockResolvedValue({
        data: {
          items: [
            { id: "1", timestamp: "2026-04-18T12:00:00Z", width: 3, height: 2 },
            { id: "2", timestamp: "2026-03-22T08:00:00Z", width: 4, height: 3 },
            { id: "3", timestamp: "2026-04-19T08:00:00Z", width: 1, height: 1 },
          ],
          next_offset: null,
        },
        error: undefined,
      }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadInitial();
    expect(store.months.map((m) => m.key)).toEqual(["2026-04", "2026-03"]);
    expect(store.months[0]?.items.length).toBe(2);
    expect(store.months[1]?.items.length).toBe(1);
  });
});

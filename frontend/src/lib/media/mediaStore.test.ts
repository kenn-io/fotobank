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

  it("preserves prior months across multiple loadMore calls", async () => {
    const page1 = {
      data: {
        items: [
          { id: "a", timestamp: "2026-04-18T12:00:00Z", width: 3, height: 2 },
          { id: "b", timestamp: "2026-04-10T08:00:00Z", width: 4, height: 3 },
        ],
        next_offset: 200,
      },
      error: undefined,
    };
    const page2 = {
      data: {
        items: [
          { id: "c", timestamp: "2026-03-22T08:00:00Z", width: 4, height: 3 },
          { id: "d", timestamp: "2026-03-01T08:00:00Z", width: 1, height: 1 },
        ],
        next_offset: null,
      },
      error: undefined,
    };
    const fakeClient = {
      GET: vi.fn().mockResolvedValueOnce(page1).mockResolvedValueOnce(page2),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    await store.loadMore();
    expect(store.months.map((m) => m.key)).toEqual(["2026-04", "2026-03"]);
    expect(store.months[0]?.items.map((i) => i.id).sort()).toEqual(["a", "b"]);
    expect(store.months[1]?.items.map((i) => i.id).sort()).toEqual(["c", "d"]);
  });

  it("sets exhausted when next_offset is null and ignores subsequent loads", async () => {
    const fakeClient = {
      GET: vi.fn().mockResolvedValue({
        data: {
          items: [{ id: "1", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1 }],
          next_offset: null,
        },
        error: undefined,
      }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    expect(store.exhausted).toBe(true);
    await store.loadMore();
    await store.loadMore();
    expect(fakeClient.GET).toHaveBeenCalledTimes(1);
  });

  it("falls back to imported_at when timestamp is missing", async () => {
    const fakeClient = {
      GET: vi.fn().mockResolvedValue({
        data: {
          items: [
            { id: "1", imported_at: "2026-02-15T08:00:00Z", width: 1, height: 1 },
          ],
          next_offset: null,
        },
        error: undefined,
      }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    expect(store.months.map((m) => m.key)).toEqual(["2026-02"]);
    expect(store.months[0]?.items[0]?.id).toBe("1");
    expect(store.months[0]?.items[0]?.timestamp).toBe("2026-02-15T08:00:00Z");
  });

  it("rejects records missing both timestamp and imported_at", async () => {
    const fakeClient = {
      GET: vi.fn().mockResolvedValue({
        data: {
          items: [
            { id: "1", width: 1, height: 1 },
            { id: "2", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1 },
          ],
          next_offset: null,
        },
        error: undefined,
      }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    expect(store.months.length).toBe(1);
    expect(store.months[0]?.items.map((i) => i.id)).toEqual(["2"]);
  });

  it("builds thumbUrl with size=grid and v=thumb_version", async () => {
    // Backend (internal/httpapi/media_thumb.go) returns 404 unless ?v=
    // matches the row's thumb_version, so the store MUST surface
    // thumb_version in the URL. Defaults to v=0 when the field is
    // absent/invalid (server then 404s, exposing the data gap).
    const fakeClient = {
      GET: vi.fn().mockResolvedValue({
        data: {
          items: [
            { id: "x", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 7 },
            { id: "y", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1 },
          ],
          next_offset: null,
        },
        error: undefined,
      }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    const items = store.months[0]?.items ?? [];
    const byId = new Map(items.map((i) => [i.id, i.thumbUrl]));
    expect(byId.get("x")).toBe("/api/v1/media/x/thumb?size=grid&v=7");
    expect(byId.get("y")).toBe("/api/v1/media/y/thumb?size=grid&v=0");
  });

  it("requests sort_desc=true so the library opens at the most recent capture", async () => {
    // The list endpoint defaults to ascending order; without sort_desc
    // the user lands on their oldest photos.
    const fakeClient = {
      GET: vi.fn().mockResolvedValue({
        data: { items: [], next_offset: null },
        error: undefined,
      }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    expect(fakeClient.GET).toHaveBeenCalledTimes(1);
    const call = fakeClient.GET.mock.calls[0];
    expect(call?.[1]?.params?.query?.sort_desc).toBe(true);
  });

  it("dedupes overlapping ids across pages", async () => {
    const page1 = {
      data: {
        items: [
          { id: "1", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1 },
          { id: "2", timestamp: "2026-04-10T12:00:00Z", width: 1, height: 1 },
        ],
        next_offset: 200,
      },
      error: undefined,
    };
    const page2 = {
      data: {
        items: [
          { id: "1", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1 },
          { id: "3", timestamp: "2026-04-05T12:00:00Z", width: 1, height: 1 },
        ],
        next_offset: null,
      },
      error: undefined,
    };
    const fakeClient = {
      GET: vi.fn().mockResolvedValueOnce(page1).mockResolvedValueOnce(page2),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    await store.loadMore();
    expect(store.months.length).toBe(1);
    expect(store.months[0]?.items.map((i) => i.id).sort()).toEqual(["1", "2", "3"]);
  });
});

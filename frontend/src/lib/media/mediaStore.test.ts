import { describe, it, expect, vi } from "vitest";
import { MediaStore, monthKey, toMedia } from "./mediaStore.svelte";
import type { ActiveFilters } from "../filters/activeFilters";

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

  it("dirties only the affected bucket on a row replacement", async () => {
    const page1 = {
      data: {
        items: [
          { id: "x", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
          { id: "y", timestamp: "2026-03-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
        ],
        next_offset: 200,
      },
      error: undefined,
    };
    const page2 = {
      data: {
        items: [
          { id: "x", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 7 },
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
    const before04 = store.months.find((m) => m.key === "2026-04");
    const before03 = store.months.find((m) => m.key === "2026-03");
    expect(before04?.items[0]?.thumbUrl).toBe("/api/v1/media/x/thumb?size=grid&v=1");

    await store.loadMore();
    const after04 = store.months.find((m) => m.key === "2026-04");
    const after03 = store.months.find((m) => m.key === "2026-03");

    // Affected month: row replaced with v=7, so the month ref must
    // change AND the field must update.
    expect(after04?.items[0]?.thumbUrl).toBe("/api/v1/media/x/thumb?size=grid&v=7");
    expect(after04).not.toBe(before04);

    // Unaffected month: not in page 2 at all. Its object ref MUST be
    // reused so VirtualGrid's keyed each-block can skip the chunk.
    // The old (pre-F2.0) merge always rebuilt every month ref — this
    // assertion is what locks the new dirty-tracking contract.
    expect(after03).toBe(before03);
  });

  it("relocates an item to a new month and prunes the old bucket when it empties", async () => {
    const page1 = {
      data: {
        items: [
          { id: "a", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1 },
        ],
        next_offset: 200,
      },
      error: undefined,
    };
    const page2 = {
      data: {
        items: [
          { id: "a", timestamp: "2025-12-01T12:00:00Z", width: 1, height: 1 },
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
    expect(store.months.map((m) => m.key)).toEqual(["2026-04"]);

    await store.loadMore();
    // The 2026-04 bucket lost its only row; the relocation must have
    // pruned the empty bucket.
    expect(store.months.map((m) => m.key)).toEqual(["2025-12"]);
    expect(store.months[0]?.items[0]?.id).toBe("a");
  });

  it("leaves every month object ref stable when re-merging identical input", async () => {
    const items = [
      { id: "1", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
      { id: "2", timestamp: "2026-03-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
      { id: "3", timestamp: "2026-02-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
    ];
    const fakeClient = {
      GET: vi.fn()
        .mockResolvedValueOnce({ data: { items, next_offset: 200 }, error: undefined })
        .mockResolvedValueOnce({ data: { items, next_offset: null }, error: undefined }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    const refs = new Map(store.months.map((m) => [m.key, m]));

    await store.loadMore(); // re-merge identical input

    // Every month object ref must be unchanged: identical inputs mean
    // every row's identity fields (timestamp, aspect, thumbUrl) match
    // what's already in the bucket, so nothing should be marked dirty.
    for (const m of store.months) {
      expect(m).toBe(refs.get(m.key));
    }
  });

  it("get(id) returns merged row with GPS fields populated", async () => {
    const fakeClient = {
      GET: vi.fn().mockResolvedValue({
        data: {
          items: [
            {
              id: "a",
              timestamp: "2024-06-15T14:30:00Z",
              width: 4,
              height: 3,
              thumb_version: 2,
              latitude: 40.7128,
              longitude: -74.006,
              gps_at: "2024-06-15T14:30:00Z",
              location_label: "New York, USA",
            },
            {
              id: "b",
              timestamp: "2024-06-15T15:00:00Z",
              width: 1,
              height: 1,
              thumb_version: 0,
            },
          ],
          next_offset: null,
        },
        error: undefined,
      }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadInitial();

    const a = store.get("a");
    expect(a?.id).toBe("a");
    expect(a?.thumbVersion).toBe(2);
    expect(a?.latitude).toBe(40.7128);
    expect(a?.longitude).toBe(-74.006);
    expect(a?.gps_at).toBe("2024-06-15T14:30:00Z");
    expect(a?.location_label).toBe("New York, USA");

    // Row without GPS fields: all four optional fields must be undefined.
    const b = store.get("b");
    expect(b?.id).toBe("b");
    expect(b?.thumbVersion).toBe(0);
    expect(b?.latitude).toBeUndefined();
    expect(b?.longitude).toBeUndefined();
    expect(b?.gps_at).toBeUndefined();
    expect(b?.location_label).toBeUndefined();
  });

  it("get(id) returns undefined for an unknown id", () => {
    const fakeClient = { GET: vi.fn() };
    const store = new MediaStore(fakeClient as never);
    expect(store.get("missing")).toBeUndefined();
  });

  it("mergeRaw populates byMediaId from a raw API row", () => {
    const store = new MediaStore({ GET: vi.fn() } as never);
    store.mergeRaw([
      { id: "x", timestamp: "2024-06-15T14:30:00Z", width: 1, height: 1, thumb_version: 5 },
    ]);
    expect(store.get("x")?.thumbVersion).toBe(5);
  });

  it("merge updates byMediaId on subsequent merges", async () => {
    const page1 = {
      data: {
        items: [
          { id: "a", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 },
        ],
        next_offset: 200,
      },
      error: undefined,
    };
    const page2 = {
      data: {
        items: [
          { id: "a", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 2 },
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
    expect(store.get("a")?.thumbVersion).toBe(1);

    await store.loadMore();
    expect(store.get("a")?.thumbVersion).toBe(2);
    expect(store.get("a")?.thumbUrl).toBe("/api/v1/media/a/thumb?size=grid&v=2");
  });
});

describe("toMedia for paired rows", () => {
  it("populates paired_with_id, paired_with, and sidecars", () => {
    const raw = {
      id: "p",
      owner_hub: "h",
      owner_user_id: "u",
      type: "photo",
      mime_type: "image/jpeg",
      original_filename: "IMG_1.JPG",
      timestamp: null,
      imported_at: "2024-06-15T14:30:00Z",
      thumb_status: "ready",
      thumb_version: 1,
      sidecars: [
        {
          id: "s",
          owner_hub: "h",
          owner_user_id: "u",
          type: "photo",
          mime_type: "image/x-adobe-dng",
          original_filename: "IMG_1.DNG",
          timestamp: null,
          imported_at: "2024-06-15T14:30:00Z",
          thumb_status: "ready",
          thumb_version: 1,
          paired_with_id: "p",
          paired_with: { id: "p", original_filename: "IMG_1.JPG" },
        },
      ],
    };
    const m = toMedia(raw);
    expect(m).not.toBeNull();
    expect(m?.sidecars).toBeDefined();
    expect(m?.sidecars).toHaveLength(1);
    expect(m?.sidecars?.[0]?.paired_with_id).toBe("p");
    expect(m?.sidecars?.[0]?.paired_with?.id).toBe("p");
  });

  it("filters out hidden sidecars from media.sidecars", () => {
    // Finding #3: a hidden sidecar must not appear in the primary's
    // sidecars list — it belongs only in HiddenMediaStore.
    const raw = {
      id: "p",
      timestamp: "2024-06-15T14:30:00Z",
      width: 1,
      height: 1,
      sidecars: [
        {
          id: "visible-sidecar",
          timestamp: "2024-06-15T14:30:00Z",
          width: 1,
          height: 1,
          paired_with_id: "p",
          // no hidden_at — visible
        },
        {
          id: "hidden-sidecar",
          timestamp: "2024-06-15T14:30:00Z",
          width: 1,
          height: 1,
          paired_with_id: "p",
          hidden_at: "2024-06-20T10:00:00Z",
        },
      ],
    };
    const m = toMedia(raw);
    expect(m?.sidecars).toHaveLength(1);
    expect(m?.sidecars?.[0]?.id).toBe("visible-sidecar");
  });

  it("strips nested sidecars on the recursive call so a future backend leak can't hide deltas", () => {
    // The backend contract is sidecars are exactly one level deep:
    // a primary embeds sidecars, but each sidecar's own Sidecars is
    // nil. sidecarsShallowEqual only inspects the top-level array, so a
    // nested array on a sidecar would silently bypass dirty-tracking.
    // The recursive toMedia call must drop the inner field so the
    // mapped sidecar.sidecars is undefined regardless of input.
    const raw = {
      id: "p",
      timestamp: "2024-06-15T14:30:00Z",
      width: 1,
      height: 1,
      sidecars: [
        {
          id: "s",
          timestamp: "2024-06-15T14:30:00Z",
          width: 1,
          height: 1,
          paired_with_id: "p",
          // Should never appear in real responses, but if a future
          // backend bug leaks it the strip in toMedia keeps the
          // merge guard's id-list comparison honest.
          sidecars: [
            { id: "leaked", timestamp: "2024-06-15T14:30:00Z", width: 1, height: 1 },
          ],
        },
      ],
    };
    const m = toMedia(raw);
    expect(m?.sidecars).toHaveLength(1);
    expect(m?.sidecars?.[0]?.id).toBe("s");
    expect(m?.sidecars?.[0]?.paired_with_id).toBe("p");
    expect(m?.sidecars?.[0]?.sidecars).toBeUndefined();
  });
});

describe("MediaStore hidden invariant", () => {
  const visibleRaw = {
    id: "v",
    timestamp: "2026-04-18T12:00:00Z",
    width: 1,
    height: 1,
    thumb_version: 1,
  };
  const hiddenRaw = {
    ...visibleRaw,
    id: "h",
    hidden_at: "2026-04-20T10:00:00Z",
  };

  it("hidden_at field is present on Media type", () => {
    // Runtime check: toMedia must pass hidden_at through.
    const m = toMedia({ ...visibleRaw, hidden_at: "2026-04-20T10:00:00Z" });
    expect(m).not.toBeNull();
    expect(m?.hidden_at).toBe("2026-04-20T10:00:00Z");
  });

  it("mergeRaw skips raw hidden rows — nothing lands in any index", () => {
    const store = new MediaStore({ GET: vi.fn() } as never);
    store.mergeRaw([hiddenRaw]);
    expect(store.get("h")).toBeUndefined();
    expect(store.months).toHaveLength(0);
  });

  it("visible row re-merged as hidden is evicted from all indexes", () => {
    const store = new MediaStore({ GET: vi.fn() } as never);
    // First merge: visible
    store.mergeRaw([visibleRaw]);
    expect(store.get("v")).toBeDefined();
    expect(store.months).toHaveLength(1);

    // Second merge: same id but now hidden
    store.mergeRaw([{ ...visibleRaw, hidden_at: "2026-04-20T10:00:00Z" }]);
    expect(store.get("v")).toBeUndefined();
    expect(store.months).toHaveLength(0);
  });

  it("removeMany evicts from all indexes", () => {
    const store = new MediaStore({ GET: vi.fn() } as never);
    store.mergeRaw([
      visibleRaw,
      { id: "w", timestamp: "2026-04-19T12:00:00Z", width: 1, height: 1 },
    ]);
    expect(store.months[0]?.items).toHaveLength(2);

    store.removeMany(["v"], "2026-04-22T00:00:00Z");
    expect(store.get("v")).toBeUndefined();
    // "w" must still be present
    expect(store.get("w")).toBeDefined();
    expect(store.months[0]?.items.map((i) => i.id)).toEqual(["w"]);
  });

  it("removeMany evicts all ids and prunes empty month buckets", () => {
    const store = new MediaStore({ GET: vi.fn() } as never);
    store.mergeRaw([visibleRaw]);
    store.removeMany(["v"], "2026-04-22T00:00:00Z");
    expect(store.months).toHaveLength(0);
    expect(store.get("v")).toBeUndefined();
  });

  it("removeMany emits media:hidden event with ids and hiddenAt", () => {
    const store = new MediaStore({ GET: vi.fn() } as never);
    store.mergeRaw([visibleRaw]);

    const received: Array<{ ids: string[]; hiddenAt: string }> = [];
    store.on("media:hidden", (payload) => received.push(payload));

    const hiddenAt = "2026-04-22T00:00:00Z";
    store.removeMany(["v"], hiddenAt);

    expect(received).toHaveLength(1);
    expect(received[0]?.ids).toEqual(["v"]);
    expect(received[0]?.hiddenAt).toBe(hiddenAt);
  });

  it("removeMany on unknown ids is a no-op and still emits the event", () => {
    const store = new MediaStore({ GET: vi.fn() } as never);
    const received: Array<{ ids: string[]; hiddenAt: string }> = [];
    store.on("media:hidden", (payload) => received.push(payload));

    store.removeMany(["missing"], "2026-04-22T00:00:00Z");
    expect(store.months).toHaveLength(0);
    expect(received).toHaveLength(1);
  });

  it("off() unregisters a media:hidden listener", () => {
    const store = new MediaStore({ GET: vi.fn() } as never);
    store.mergeRaw([visibleRaw]);

    const received: Array<{ ids: string[]; hiddenAt: string }> = [];
    const handler = (payload: { ids: string[]; hiddenAt: string }) =>
      received.push(payload);
    store.on("media:hidden", handler);
    store.off("media:hidden", handler);

    store.removeMany(["v"], "2026-04-22T00:00:00Z");
    expect(received).toHaveLength(0);
  });
});

describe("MediaStore merge with sidecars", () => {
  it("does not dirty the bucket when merging identical primary+sidecar input twice", async () => {
    // The merge contract observable: month object refs are stable
    // across an identical re-merge. If sidecarsShallowEqual returns
    // true for the same input, the bucket isn't marked dirty and the same
    // Month object is reused. This is the same hook the existing
    // "leaves every month object ref stable" test uses for primaries.
    const item = {
      id: "p",
      timestamp: "2024-06-15T14:30:00Z",
      width: 1,
      height: 1,
      thumb_version: 1,
      sidecars: [
        {
          id: "s",
          timestamp: "2024-06-15T14:30:00Z",
          width: 1,
          height: 1,
          thumb_version: 1,
          paired_with_id: "p",
          paired_with: { id: "p", original_filename: "IMG_1.JPG" },
        },
      ],
    };
    const fakeClient = {
      GET: vi.fn()
        .mockResolvedValueOnce({ data: { items: [item], next_offset: 200 }, error: undefined })
        .mockResolvedValueOnce({ data: { items: [item], next_offset: null }, error: undefined }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    const before = store.months.find((m) => m.key === "2024-06");
    expect(before?.items[0]?.sidecars).toHaveLength(1);

    await store.loadMore();
    const after = store.months.find((m) => m.key === "2024-06");
    expect(after).toBe(before);
  });

  it("dirties the bucket when a sidecar's original_filename changes", async () => {
    // A rename on the sidecar must propagate to the UI: MediaDetail's
    // Files row reads sidecar.original_filename, so if the merge guard
    // only compared ids the bucket would not be marked dirty and the
    // stored Media would still carry the old filename. The visible
    // hook is the same one the existing identical-input test uses:
    // when the bucket is dirtied the month object ref is rebuilt, so
    // we assert the new ref is NOT the previous one.
    const page1Item = {
      id: "p",
      timestamp: "2024-06-15T14:30:00Z",
      width: 1,
      height: 1,
      thumb_version: 1,
      sidecars: [
        {
          id: "s",
          timestamp: "2024-06-15T14:30:00Z",
          width: 1,
          height: 1,
          thumb_version: 1,
          original_filename: "IMG_1.DNG",
          paired_with_id: "p",
          paired_with: { id: "p", original_filename: "IMG_1.JPG" },
        },
      ],
    };
    const page2Item = {
      ...page1Item,
      sidecars: [
        {
          ...page1Item.sidecars[0],
          original_filename: "IMG_1_renamed.DNG",
        },
      ],
    };
    const fakeClient = {
      GET: vi.fn()
        .mockResolvedValueOnce({ data: { items: [page1Item], next_offset: 200 }, error: undefined })
        .mockResolvedValueOnce({ data: { items: [page2Item], next_offset: null }, error: undefined }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadMore();
    const before = store.months.find((m) => m.key === "2024-06");
    expect(before?.items[0]?.sidecars?.[0]?.original_filename).toBe("IMG_1.DNG");

    await store.loadMore();
    const after = store.months.find((m) => m.key === "2024-06");
    expect(after).not.toBe(before);
    expect(after?.items[0]?.sidecars?.[0]?.original_filename).toBe("IMG_1_renamed.DNG");
  });
});

describe("MediaStore filter-change reset protocol", () => {
  // The store's wire-format is { items, next_offset }; the mocked GET
  // here keys items off the camera filter so we can prove the URL
  // params actually reach the request.
  type Row = {
    id: string;
    timestamp: string;
    width: number;
    height: number;
    thumb_version: number;
  };
  function rowsByCameraClient(rowsByCamera: Record<string, Row[]>) {
    const GET = vi.fn(
      async (
        _path: string,
        opts: { params?: { query?: { camera?: string[] } } },
      ) => {
        const cam = opts.params?.query?.camera?.[0] ?? "*";
        return {
          data: { items: rowsByCamera[cam] ?? [], next_offset: null },
          error: undefined,
        };
      },
    );
    return { GET };
  }

  const emptyFilters: ActiveFilters = {
    cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
  };

  it("clears months on filter change and refetches", async () => {
    const client = rowsByCameraClient({
      "*": [{ id: "any-1", timestamp: "2025-01-01T00:00:00Z", width: 3, height: 2, thumb_version: 1 }],
      "Sony A7R IV": [{ id: "sony-1", timestamp: "2025-01-01T00:00:00Z", width: 3, height: 2, thumb_version: 1 }],
    });
    const store = new MediaStore(client as never);
    await store.loadInitial();
    expect(store.get("any-1")).toBeDefined();

    store.setFilters({ ...emptyFilters, cameras: ["Sony A7R IV"] });
    await store.loadInitial();

    expect(store.get("any-1")).toBeUndefined(); // cleared
    expect(store.get("sony-1")).toBeDefined();
    expect(store.months).toHaveLength(1);
  });

  it("setFilters with same filterKey is a no-op (preserves cached rows)", async () => {
    const client = rowsByCameraClient({
      "*": [{ id: "x", timestamp: "2025-01-01T00:00:00Z", width: 1, height: 1, thumb_version: 1 }],
    });
    const store = new MediaStore(client as never);
    await store.loadInitial();
    expect(client.GET).toHaveBeenCalledTimes(1);

    // Same key, different array refs — must not clear or trigger any side effect.
    store.setFilters({ cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null });
    expect(store.get("x")).toBeDefined();
    expect(client.GET).toHaveBeenCalledTimes(1);
  });

  it("late stale response is dropped after setFilters bumps fetchToken", async () => {
    let resolveStale: (v: unknown) => void = () => {};
    const stale = new Promise((r) => (resolveStale = r));
    const fastResponse = {
      data: {
        items: [
          { id: "fast-1", timestamp: "2025-01-01T00:00:00Z", width: 1, height: 1, thumb_version: 1 },
        ],
        next_offset: null,
      },
      error: undefined,
    };
    let call = 0;
    const client = {
      GET: vi.fn(async () => (call++ === 0 ? await stale : fastResponse)),
    };
    const store = new MediaStore(client as never);

    const p1 = store.loadInitial();
    store.setFilters({ ...emptyFilters, cameras: ["X"] });
    const p2 = store.loadInitial();
    await p2;
    resolveStale({
      data: {
        items: [
          { id: "STALE-1", timestamp: "2025-01-01T00:00:00Z", width: 1, height: 1, thumb_version: 1 },
        ],
        next_offset: null,
      },
      error: undefined,
    });
    await p1;
    expect(store.get("STALE-1")).toBeUndefined();
    expect(store.get("fast-1")).toBeDefined();
  });

  it("sends camera/lens/facet_tag/has_gps/media_type query params on the GET", async () => {
    // Forward-leaning: /api/v1/media doesn't honor these yet (SF-17 lands
    // backend support); huma silently ignores unknown params for now.
    const client = {
      GET: vi.fn().mockResolvedValue({
        data: { items: [], next_offset: null },
        error: undefined,
      }),
    };
    const store = new MediaStore(client as never);
    store.setFilters({
      cameras: ["Sony A7R IV", "Canon EOS R5"],
      lenses: ["FE 50mm F1.8"],
      tagKeys: ["place:nyc"],
      hasGps: true,
      mediaType: "photo",
    });
    await store.loadInitial();

    const call = client.GET.mock.calls[0];
    const query = call?.[1]?.params?.query as Record<string, unknown> | undefined;
    expect(query?.["camera"]).toEqual(["Sony A7R IV", "Canon EOS R5"]);
    expect(query?.["lens"]).toEqual(["FE 50mm F1.8"]);
    expect(query?.["facet_tag"]).toEqual(["place:nyc"]);
    // huma expects the literal "true"/"false" strings for *bool query
    // params (matches facetsStore convention, see SF-10).
    expect(query?.["has_gps"]).toBe("true");
    expect(query?.["media_type"]).toBe("photo");
  });

  it("sends has_gps=false when hasGps is false", async () => {
    const client = {
      GET: vi.fn().mockResolvedValue({
        data: { items: [], next_offset: null },
        error: undefined,
      }),
    };
    const store = new MediaStore(client as never);
    store.setFilters({ ...emptyFilters, hasGps: false });
    await store.loadInitial();

    const query = client.GET.mock.calls[0]?.[1]?.params?.query as
      | Record<string, unknown>
      | undefined;
    expect(query?.["has_gps"]).toBe("false");
  });

  it("omits filter params when the filters are empty", async () => {
    const client = {
      GET: vi.fn().mockResolvedValue({
        data: { items: [], next_offset: null },
        error: undefined,
      }),
    };
    const store = new MediaStore(client as never);
    await store.loadInitial();

    const query = client.GET.mock.calls[0]?.[1]?.params?.query as
      | Record<string, unknown>
      | undefined;
    expect(query?.["camera"]).toBeUndefined();
    expect(query?.["lens"]).toBeUndefined();
    expect(query?.["facet_tag"]).toBeUndefined();
    expect(query?.["has_gps"]).toBeUndefined();
    expect(query?.["media_type"]).toBeUndefined();
  });

  it("setFilters resets pagination so loadMore restarts at offset 0", async () => {
    const page1 = {
      data: {
        items: [{ id: "a", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 }],
        next_offset: 200,
      },
      error: undefined,
    };
    const page2AfterReset = {
      data: {
        items: [{ id: "b", timestamp: "2026-04-18T12:00:00Z", width: 1, height: 1, thumb_version: 1 }],
        next_offset: null,
      },
      error: undefined,
    };
    const client = {
      GET: vi.fn().mockResolvedValueOnce(page1).mockResolvedValueOnce(page2AfterReset),
    };
    const store = new MediaStore(client as never);
    await store.loadInitial();
    expect(client.GET.mock.calls[0]?.[1]?.params?.query?.offset).toBe(0);

    store.setFilters({ ...emptyFilters, cameras: ["X"] });
    await store.loadInitial();

    // After reset, offset must be 0 again — not the leftover 200.
    expect(client.GET.mock.calls[1]?.[1]?.params?.query?.offset).toBe(0);
    expect(store.get("a")).toBeUndefined();
    expect(store.get("b")).toBeDefined();
  });
});

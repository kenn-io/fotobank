import { describe, expect, it } from "vitest";
import { GeoStore } from "./geoStore.svelte";
import type { Client } from "../api/client";

type Resp = { data?: unknown; error?: unknown };

function makeClient(byUrl: Record<string, Resp>) {
  const calls: Array<{ path: string; params?: unknown }> = [];
  const handler = async (path: string, opts: Record<string, unknown> = {}) => {
    calls.push({ path, params: (opts as { params?: unknown }).params });
    return byUrl[path] ?? { error: { status: 500 } };
  };
  return { GET: handler, calls };
}

const sampleItem = {
  id: "a",
  timestamp: "2024-06-15T14:30:22Z",
  width: 1920,
  height: 1080,
  thumb_version: 0,
  latitude: 1,
  longitude: 2,
};

describe("GeoStore", () => {
  it("loads visible-only by default and exposes items", async () => {
    const client = makeClient({
      "/api/v1/media/geo": {
        data: { items: [sampleItem] },
      },
    });
    const s = new GeoStore(client as unknown as Pick<Client, "GET">);
    await s.load(false);
    expect(s.items.length).toBe(1);
    expect(s.includedHiddenAtFetch).toBe(false);
    expect(s.ready).toBe(true);
  });

  it("loads with include_hidden=true when requested and records the flag", async () => {
    const client = makeClient({
      "/api/v1/media/geo": {
        data: { items: [{ ...sampleItem, id: "h" }] },
      },
    });
    const s = new GeoStore(client as unknown as Pick<Client, "GET">);
    await s.load(true);
    expect(s.items.length).toBe(1);
    expect(s.includedHiddenAtFetch).toBe(true);
    // Verify the include_hidden=true query was passed:
    expect(client.calls[0]?.params).toMatchObject({ query: { include_hidden: true } });
  });

  it("findById returns the cached item or undefined", async () => {
    const client = makeClient({
      "/api/v1/media/geo": {
        data: { items: [sampleItem] },
      },
    });
    const s = new GeoStore(client as unknown as Pick<Client, "GET">);
    await s.load(false);
    expect(s.findById("a")?.latitude).toBe(1);
    expect(s.findById("missing")).toBeUndefined();
  });

  it("treats HTTP failure as resolved-with-error (does not throw)", async () => {
    const client = makeClient({
      "/api/v1/media/geo": { error: { status: 500 } },
    });
    const s = new GeoStore(client as unknown as Pick<Client, "GET">);
    await s.load(false);
    expect(s.items).toEqual([]);
    expect(s.error).not.toBeNull();
    expect(s.ready).toBe(false);
  });

  it("ignores stale responses when a newer load() is in flight", async () => {
    // Two queued resolvers: the first call resolves AFTER the second so
    // the older request's response would clobber the newer one without
    // a request-token guard.
    let resolveOld: (v: { data: { items: unknown[] } }) => void = () => {};
    let resolveNew: (v: { data: { items: unknown[] } }) => void = () => {};
    let call = 0;
    const client = {
      GET: () => {
        call++;
        if (call === 1) return new Promise((r) => (resolveOld = r));
        return new Promise((r) => (resolveNew = r));
      },
    } as unknown as Pick<Client, "GET">;
    const s = new GeoStore(client);
    const oldP = s.load(false);
    const newP = s.load(true);
    // Newer call resolves first with the include_hidden dataset.
    resolveNew({ data: { items: [{ ...sampleItem, id: "new", hidden_at: "x" }] } });
    await newP;
    expect(s.items.map((m) => m.id)).toEqual(["new"]);
    expect(s.includedHiddenAtFetch).toBe(true);
    // Older call resolves now with stale data — must be discarded.
    resolveOld({ data: { items: [{ ...sampleItem, id: "old" }] } });
    await oldP;
    expect(s.items.map((m) => m.id)).toEqual(["new"]);
    expect(s.includedHiddenAtFetch).toBe(true);
  });

  it("retains raw items so callers can mergeRaw into MediaStore", async () => {
    const raw = { ...sampleItem, id: "r1" };
    const client = makeClient({
      "/api/v1/media/geo": { data: { items: [raw] } },
    });
    const s = new GeoStore(client as unknown as Pick<Client, "GET">);
    await s.load(false);
    expect(s.rawItems).toEqual([raw]);
  });

  it("forwards camera/lens/facet_tag/media_type into the query", async () => {
    const client = makeClient({
      "/api/v1/media/geo": { data: { items: [sampleItem] } },
    });
    const s = new GeoStore(client as unknown as Pick<Client, "GET">);
    await s.load(false, {
      cameras: ["Sony A7R IV"],
      lenses: ["Sony FE 24-70 GM"],
      facetTags: ["dog", "beach"],
      mediaType: "photo",
    });
    expect(client.calls[0]?.params).toMatchObject({
      query: {
        camera: ["Sony A7R IV"],
        lens: ["Sony FE 24-70 GM"],
        facet_tag: ["dog", "beach"],
        media_type: "photo",
      },
    });
  });

  it("clears stale items when filters narrow between loads", async () => {
    // First load returns two pins under the unfiltered set; the second
    // load narrows to a single camera and the cache must be cleared
    // synchronously between the two so a slow render doesn't flash
    // the old pins under the new filter.
    const calls: Array<Record<string, unknown>> = [];
    let next = 0;
    const responses = [
      { data: { items: [sampleItem, { ...sampleItem, id: "b" }] } },
      { data: { items: [{ ...sampleItem, id: "b" }] } },
    ];
    const client = {
      GET: async (_path: string, opts?: { params?: { query?: Record<string, unknown> } }) => {
        calls.push(opts?.params?.query ?? {});
        return responses[next++] ?? { error: { status: 500 } };
      },
    };
    const s = new GeoStore(client as unknown as Pick<Client, "GET">);
    await s.load(false);
    expect(s.items.length).toBe(2);

    const p = s.load(false, { cameras: ["Sony A7R IV"] });
    // Synchronous reset: items must be empty BEFORE the second response
    // resolves so the UI doesn't transiently render the wider set.
    expect(s.items.length).toBe(0);
    expect(s.ready).toBe(false);
    await p;
    expect(s.items.length).toBe(1);
    expect(calls[1]).toMatchObject({ camera: ["Sony A7R IV"] });
  });
});

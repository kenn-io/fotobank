import { describe, it, expect, vi } from "vitest";
import { FacetsStore, type FacetsResponse } from "./facetsStore.svelte";
import type { ActiveFilters } from "./activeFilters";

const empty: ActiveFilters = {
  cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
};

const fakeResponse: FacetsResponse = {
  cameras: [{ value: "Sony A7R IV", count: 5 }],
  lenses: [], tags: [], places: { with_gps: 0, without_gps: 0 }, media_types: [],
};

function fakeClient(seq: FacetsResponse[]) {
  const calls: string[] = [];
  let i = 0;
  const GET = vi.fn(async (path: string) => {
    calls.push(path);
    return { data: seq[Math.min(i++, seq.length - 1)] };
  });
  return { client: { GET }, calls };
}

describe("FacetsStore", () => {
  it("fetches and stores response for a filter set", async () => {
    const { client } = fakeClient([fakeResponse]);
    const s = new FacetsStore(client as never, /*debounceMs=*/ 0);
    await s.fetch("library", empty);
    expect(s.response).toEqual(fakeResponse);
  });

  it("caches by (route + filters); same key skips refetch", async () => {
    const { client, calls } = fakeClient([fakeResponse]);
    const s = new FacetsStore(client as never, 0);
    await s.fetch("library", empty);
    await s.fetch("library", empty);
    expect(calls.length).toBe(1);
  });

  it("different routes use different cache slots", async () => {
    const { client, calls } = fakeClient([fakeResponse, fakeResponse]);
    const s = new FacetsStore(client as never, 0);
    await s.fetch("library", empty);
    await s.fetch("search", empty);
    expect(calls.length).toBe(2);
  });

  it("rapid filter changes coalesce via debounce", async () => {
    const { client, calls } = fakeClient([fakeResponse, fakeResponse]);
    const s = new FacetsStore(client as never, 50);
    s.fetch("library", empty);
    s.fetch("library", { ...empty, cameras: ["Sony A7R IV"] });
    s.fetch("library", { ...empty, cameras: ["Canon EOS R5"] });
    await new Promise((r) => setTimeout(r, 100));
    // Only the final request actually fires.
    expect(calls.length).toBe(1);
    expect(calls[0]).toContain("camera=Canon");
  });

  it("stale responses are dropped", async () => {
    let resolveFirst: (v: FacetsResponse) => void = () => {};
    const slow = new Promise<FacetsResponse>((r) => (resolveFirst = r));
    const fast = Promise.resolve(fakeResponse);
    let i = 0;
    const GET = vi.fn(async () => ({ data: i++ === 0 ? await slow : await fast }));
    const s = new FacetsStore({ GET } as never, 0);

    const p1 = s.fetch("library", empty);
    // Yield so the first debounce timer (debounceMs=0) actually fires
    // and the slow GET is in flight before the second fetch is queued.
    // Without this yield the second fetch's clearTimeout coalesces both
    // into a single GET, so we never get two GETs racing — which is the
    // condition this test exercises.
    await new Promise((r) => setTimeout(r, 0));
    const p2 = s.fetch("library", { ...empty, cameras: ["Sony A7R IV"] });
    await p2;
    // Now resolve the first (stale) request — must NOT overwrite store.
    resolveFirst({ ...fakeResponse, cameras: [{ value: "STALE", count: 1 }] });
    await p1;
    // Yield once more so the stale GET's continuation runs. Without
    // this, the assertion fires before the stale-drop branch executes
    // and a regression (e.g. removing the token check) could pass.
    await new Promise((r) => setTimeout(r, 0));
    expect(s.response?.cameras[0]?.value).not.toBe("STALE");
  });
});

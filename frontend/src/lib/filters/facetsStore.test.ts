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

  it("cache hit during in-flight GET clears loading (no stuck spinner)", async () => {
    // Race: A's GET is in flight (loading=true). Then B comes in for
    // a key that's already cached (cache hit). The cache-hit path
    // bumps fetchToken to invalidate A, but A's finally then sees
    // myToken !== fetchToken and skips its loading=false cleanup.
    // The cache-hit branch must clear loading explicitly or the
    // FilterSidebar would render a permanent spinner.
    let resolveA: (v: FacetsResponse) => void = () => {};
    const slowA = new Promise<FacetsResponse>((r) => (resolveA = r));
    const respA: FacetsResponse = {
      cameras: [{ value: "A-CAM", count: 1 }],
      lenses: [], tags: [],
      places: { with_gps: 0, without_gps: 0 }, media_types: [],
    };
    const respB: FacetsResponse = {
      cameras: [{ value: "B-CAM", count: 2 }],
      lenses: [], tags: [],
      places: { with_gps: 0, without_gps: 0 }, media_types: [],
    };
    const GET = vi.fn(async () => ({ data: await slowA }));
    const s = new FacetsStore({ GET } as never, 0);

    // Pre-warm cache for filterB by issuing a quick GET that resolves
    // immediately, then clearing the GET fake.
    (s as unknown as {
      cache: Map<string, FacetsResponse>;
    }).cache.set(`library|${JSON.stringify({
      cameras: ["B-CAM"], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
    })}`, respB);

    // Now start A (slow GET).
    const pA = s.fetch("library", empty);
    // Yield so A's debounce timer fires and the slow GET is in flight,
    // setting loading=true.
    await new Promise((r) => setTimeout(r, 0));
    expect(s.loading).toBe(true);

    // Now hit cache for filterB. This must clear loading.
    await s.fetch("library", { ...empty, cameras: ["B-CAM"] });
    expect(s.loading).toBe(false);
    expect(s.response?.cameras[0]?.value).toBe("B-CAM");

    // Resolve A. Its finally sees stale token and skips cleanup —
    // loading remains false (already cleared by the cache-hit branch).
    resolveA(respA);
    await pA;
    expect(s.loading).toBe(false);
  });

  it("a newer fetch awaits the newer response, not an earlier in-flight one", async () => {
    // Race the reviewer flagged: caller B arrives while caller A's GET
    // is in flight. Without the synchronous fetchToken bump at schedule
    // time, A's settle path would drain B's resolver early — letting
    // B see A's response in this.response. With the fix, A's myToken
    // becomes stale the moment B schedules a new timer, A's finally
    // skips the resolver drain, and B's resolver settles only when
    // B's own fetch lands its body.
    let resolveA: (v: FacetsResponse) => void = () => {};
    const slowA = new Promise<FacetsResponse>((r) => (resolveA = r));
    const respA: FacetsResponse = {
      cameras: [{ value: "A-CAM", count: 1 }],
      lenses: [], tags: [],
      places: { with_gps: 0, without_gps: 0 }, media_types: [],
    };
    const respB: FacetsResponse = {
      cameras: [{ value: "B-CAM", count: 2 }],
      lenses: [], tags: [],
      places: { with_gps: 0, without_gps: 0 }, media_types: [],
    };
    let i = 0;
    const GET = vi.fn(async (_path: string) => {
      if (i++ === 0) {
        const v = await slowA;
        return { data: v };
      }
      return { data: respB };
    });
    const s = new FacetsStore({ GET } as never, 0);

    const pA = s.fetch("library", empty);
    // Yield so A's debounce timer fires and the slow GET is in flight.
    await new Promise((r) => setTimeout(r, 0));
    const pB = s.fetch("library", { ...empty, cameras: ["B-CAM"] });

    // Resolve A's slow GET first. With the fix, A's settle path sees
    // its myToken is stale and short-circuits without draining
    // resolvers. pB must NOT settle yet — it's waiting for B's GET.
    resolveA(respA);

    // Race B's settle vs. a 50ms watchdog. With the fix, B's GET (the
    // second iteration of the GET fake) drives B's debounce-timer
    // callback, which sets this.response = respB, then drains B's
    // resolver with the LATEST state.
    await pB;
    expect(s.response?.cameras[0]?.value).toBe("B-CAM");
    // pA also settles (eventually) because B's settle path drains
    // every queued resolver, including pA's. Both callers are now at
    // the latest state — that's the contract.
    await pA;
    expect(s.response?.cameras[0]?.value).toBe("B-CAM");
  });

  it("encodes has_gps as 'true'/'false' (not '1'/'0') to match backend", async () => {
    // /api/v1/facets accepts has_gps as literal "true"/"false"
    // (huma enum constraint, see internal/httpapi/facets.go). Sending
    // "1"/"0" would 400 — pin the canonical wire format here.
    const { client, calls } = fakeClient([fakeResponse, fakeResponse]);
    const s = new FacetsStore(client as never, 0);
    await s.fetch("library", { ...empty, hasGps: true });
    expect(calls[0]).toContain("has_gps=true");
    expect(calls[0]).not.toContain("has_gps=1");
    await s.fetch("library", { ...empty, hasGps: false });
    expect(calls[1]).toContain("has_gps=false");
  });

  it("forces has_gps=true on /map when caller hasn't pinned it", async () => {
    // /map's facet counts must scope to geotagged photos — the user
    // can't even pin a non-geotagged photo on the map.
    const { client, calls } = fakeClient([fakeResponse]);
    const s = new FacetsStore(client as never, 0);
    await s.fetch("map", empty);
    expect(calls[0]).toContain("has_gps=true");
  });

  it("populates this.error and leaves this.response unchanged on backend error", async () => {
    // openapi-fetch surfaces non-2xx on res.error (not via thrown
    // exception). Without reading res.error the store would silently
    // ignore a 400/5xx and leave a stale this.response visible.
    const seq: Array<{ data?: FacetsResponse; error?: { detail: string } }> = [
      { data: fakeResponse },
      { error: { detail: "boom" } },
    ];
    let i = 0;
    const GET = vi.fn(async () => seq[i++]);
    const s = new FacetsStore({ GET } as never, 0);
    await s.fetch("library", empty);
    expect(s.response).toEqual(fakeResponse);
    expect(s.error).toBeNull();
    await s.fetch("library", { ...empty, cameras: ["X"] });
    expect(s.error).toBe("boom");
    // Prior response stays visible — UX-side, the FilterSidebar can
    // keep showing the last-good counts while the error toast fires.
    expect(s.response).toEqual(fakeResponse);
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

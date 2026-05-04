import type { Client } from "../api/client";
import { filterKey, type ActiveFilters } from "./activeFilters";

export type FacetValue = { value: string; count: number };
export type FacetTag = { key: string; label: string; count: number };
export type FacetPlaces = { with_gps: number; without_gps: number };

export type FacetsResponse = {
  cameras: FacetValue[];
  lenses: FacetValue[];
  tags: FacetTag[];
  places: FacetPlaces;
  media_types: FacetValue[];
};

export type RouteContext = "library" | "search" | "map";

export class FacetsStore {
  response = $state<FacetsResponse | null>(null);
  loading = $state(false);
  error = $state<string | null>(null);

  private cache = new Map<string, FacetsResponse>();
  private fetchToken = 0;
  private debounceTimer: ReturnType<typeof setTimeout> | null = null;
  // pendingResolvers accumulates the promise resolvers from every
  // debounced caller while a timer is in flight. When the timer fires
  // and the fetch settles, we resolve them all together so each
  // caller's `await fetch(...)` continues only after the latest state
  // is in `this.response`. A single pendingResolve scalar would either
  // leak earlier callers (their resolver gets overwritten) or — if we
  // resolve them at clearTimeout — let them continue while state is
  // still stale.
  private pendingResolvers: Array<() => void> = [];

  constructor(
    private client: Pick<Client, "GET">,
    private debounceMs = 100,
  ) {}

  /**
   * Fetch facets for the given route + filters. Identical key → cached.
   * Multiple rapid calls coalesce into the latest one (debounced).
   * Stale responses are dropped via fetchToken.
   */
  fetch(route: RouteContext, filters: ActiveFilters): Promise<void> {
    const key = `${route}|${filterKey(filters)}`;
    const cached = this.cache.get(key);
    if (cached) {
      this.response = cached;
      return Promise.resolve();
    }

    // Rapid calls coalesce: clear the existing timer (the prior call's
    // request is now superseded) and queue this caller's resolver
    // alongside any earlier ones. They all settle together when the
    // latest scheduled fetch completes — each caller's await thus
    // continues only after `this.response` reflects the latest state.
    if (this.debounceTimer !== null) clearTimeout(this.debounceTimer);

    return new Promise<void>((resolve) => {
      this.pendingResolvers.push(resolve);
      this.debounceTimer = setTimeout(async () => {
        const myToken = ++this.fetchToken;
        this.loading = true;
        this.error = null;
        try {
          const path = this.buildPath(route, filters);
          // TODO: swap the `as never` cast for a properly-typed call
          // once /api/v1/facets lands in the generated openapi schema.
          const res = await this.client.GET(path as never);
          if (myToken !== this.fetchToken) return; // stale, drop
          // openapi-fetch wraps the body in `data`; adjust if the
          // generated client surface differs.
          const body = (res as { data?: FacetsResponse }).data;
          if (body) {
            this.cache.set(key, body);
            this.response = body;
          }
        } catch (e) {
          if (myToken !== this.fetchToken) return;
          this.error = e instanceof Error ? e.message : String(e);
        } finally {
          if (myToken === this.fetchToken) {
            this.loading = false;
            // Settle every resolver queued while this timer was in
            // flight. If a newer fetch raced ahead (myToken !==
            // fetchToken), defer to that fetch's settle path — the
            // resolvers outlive a stale closure.
            const resolvers = this.pendingResolvers;
            this.pendingResolvers = [];
            for (const r of resolvers) r();
          }
        }
      }, this.debounceMs);
    });
  }

  private buildPath(_route: RouteContext, f: ActiveFilters): string {
    const sp = new URLSearchParams();
    for (const v of f.cameras) sp.append("camera", v);
    for (const v of f.lenses) sp.append("lens", v);
    for (const v of f.tagKeys) sp.append("facet_tag", v);
    if (f.hasGps !== null) sp.set("has_gps", f.hasGps ? "1" : "0");
    if (f.mediaType !== null) sp.set("media_type", f.mediaType);
    const qs = sp.toString();
    return qs ? `/api/v1/facets?${qs}` : `/api/v1/facets`;
  }
}

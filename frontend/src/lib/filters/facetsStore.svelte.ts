import type { FacetsParams } from "../api/generated/models";
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

// SearchScope is the search-route-only narrowing surface that gets
// added to /api/v1/facets when the page is /search. /library and /map
// don't take a query / typed-tag / location / include-hidden surface,
// so the scope is omitted on those routes — facets there are computed
// across the whole library subject only to the sidebar facets.
//
// Without this scoping, /search facet counts would surface alternatives
// from the entire library, ignoring the user's q / typed tags / date
// range / location / include_hidden — see roborev finding 17964 #2.
// The /facets backend already accepts these wire params (date_after,
// date_before, tag, location, include_hidden); SF-18 just plumbs them
// from App.svelte's router.current down here.
export type SearchScope = {
  q?: string;
  dateAfter?: string;
  dateBefore?: string;
  tagLabels?: string[]; // typed-chip strip — distinct from ActiveFilters.tagKeys
  location?: string;
  includeHidden?: boolean;
};

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
    private client: Pick<Client, "facets">,
    private debounceMs = 100,
  ) {}

  /**
   * Fetch facets for the given route + filters. Identical key → cached.
   * Multiple rapid calls coalesce into the latest one (debounced).
   * Stale responses are dropped via fetchToken.
   *
   * scope is honoured only when route === "search" — /library and /map
   * don't carry a search-scope surface. Passing scope on other routes
   * is permitted but ignored, so the call site doesn't have to branch
   * on route before invoking. The cache key incorporates the scope so
   * /search?q=foo and /search?q=bar produce distinct cache slots even
   * with identical sidebar filters.
   */
  fetch(route: RouteContext, filters: ActiveFilters, scope?: SearchScope): Promise<void> {
    const effectiveScope = route === "search" ? scope : undefined;
    const key = `${route}|${filterKey(filters)}|${scopeKey(effectiveScope)}`;
    const cached = this.cache.get(key);
    if (cached) {
      // A cache hit invalidates any in-flight fetch: the user is
      // already at the latest state, so drain any queued resolvers
      // and bump the token so a still-running GET cannot overwrite
      // this.response when its promise settles later. Clear loading
      // and error explicitly: the in-flight GET's finally won't run
      // its cleanup (its myToken is now stale), so the store would
      // otherwise be stranded with loading=true forever.
      ++this.fetchToken;
      if (this.debounceTimer !== null) clearTimeout(this.debounceTimer);
      this.response = cached;
      this.loading = false;
      this.error = null;
      const resolvers = this.pendingResolvers;
      this.pendingResolvers = [];
      for (const r of resolvers) r();
      return Promise.resolve();
    }

    // Rapid calls coalesce: clear the prior debounce timer and bump
    // fetchToken SYNCHRONOUSLY so any in-flight callback from an
    // earlier timer immediately becomes stale and short-circuits in
    // its post-await checks. Without the synchronous bump, an earlier
    // fetch could resolve and drain pendingResolvers — including this
    // newer caller's resolver — with stale state still in
    // this.response.
    if (this.debounceTimer !== null) clearTimeout(this.debounceTimer);
    const myToken = ++this.fetchToken;

    return new Promise<void>((resolve) => {
      this.pendingResolvers.push(resolve);
      this.debounceTimer = setTimeout(async () => {
        // Defensive: clearTimeout in a later fetch should have
        // prevented this callback from running, but Node's timer
        // semantics leave a tiny window if a later schedule happens
        // between firing and dispatch. Re-check before any work.
        if (myToken !== this.fetchToken) return;
        this.loading = true;
        this.error = null;
        try {
          const res = await this.client.facets(this.buildQuery(route, filters, effectiveScope));
          if (myToken !== this.fetchToken) return; // stale, drop
          // the API client surfaces non-2xx responses on res.error and
          // leaves res.data undefined. Without checking res.error, a
          // server-side 400/5xx would silently leave stale this.response
          // visible. Map the huma error shape ({title, detail, status})
          // or thrown JS Error to a string and bail before writing the
          // cache.
          const r = res as {
            data?: FacetsResponse;
            error?: { detail?: string; title?: string; message?: string };
          };
          if (r.error) {
            const e = r.error;
            this.error = e.detail ?? e.message ?? e.title ?? "Failed to load facets";
          } else if (r.data) {
            this.cache.set(key, r.data);
            this.response = r.data;
          } else {
            // the API client always populates data OR error on a settled
            // response, but the type surface allows neither in principle.
            // Without an explicit fallback, the prior this.response would
            // remain visible with no error signal — the caller would
            // think the latest filter set succeeded and showed the
            // previous counts. Surface a generic error so the UI can at
            // least flag that something went wrong.
            this.error = "Facets response missing both data and error";
          }
        } catch (e) {
          if (myToken !== this.fetchToken) return;
          this.error = e instanceof Error ? e.message : String(e);
        } finally {
          if (myToken === this.fetchToken) {
            this.loading = false;
            // Settle every resolver queued while this token was the
            // latest. A newer fetch would have bumped fetchToken
            // synchronously, leaving this branch unreachable — its
            // own settle path will drain the resolvers (including
            // any pushed during our await) when it lands.
            const resolvers = this.pendingResolvers;
            this.pendingResolvers = [];
            for (const r of resolvers) r();
          }
        }
      }, this.debounceMs);
    });
  }

  private buildQuery(route: RouteContext, f: ActiveFilters, scope: SearchScope | undefined): FacetsParams {
    const query: FacetsParams = { camera: f.cameras, lens: f.lenses, facet_tag: f.tagKeys };
    if (f.hasGps !== null) query.has_gps = f.hasGps ? "true" : "false";
    else if (route === "map") query.has_gps = "true";
    if (f.mediaType !== null) query.media_type = f.mediaType;
    if (scope?.dateAfter) query.date_after = scope.dateAfter;
    if (scope?.dateBefore) query.date_before = scope.dateBefore;
    if (scope?.tagLabels) query.tag = scope.tagLabels.filter(Boolean);
    if (scope?.location) query.location = scope.location;
    if (scope?.includeHidden) query.include_hidden = true;
    return query;
  }
}

// scopeKey serialises a SearchScope into a stable JSON string so the
// FacetsStore cache differentiates /search?q=foo from /search?q=bar
// even when the sidebar filters match. Sort tagLabels for stability:
// the URL preserves user-add order, but two URLs with permuted tags
// describe the same scope and should hit the same cache slot.
function scopeKey(s: SearchScope | undefined): string {
  if (s === undefined) return "";
  return JSON.stringify({
    q: s.q ?? "",
    da: s.dateAfter ?? "",
    db: s.dateBefore ?? "",
    tl: s.tagLabels ? [...s.tagLabels].sort() : [],
    loc: s.location ?? "",
    ih: s.includeHidden === true,
  });
}

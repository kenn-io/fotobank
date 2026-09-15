import type { Client } from "../api/client";
import { filterKey, type ActiveFilters } from "../filters/activeFilters";

// thumb_status mirrors the backend enum on `media.thumb_status`. The
// grid uses it to decide what to render:
//   ready   → fetch the real thumb URL (the only state where /thumb
//             returns 200; every other state 404s)
//   pending → background worker hasn't started, show a loading shimmer
//   working → worker has claimed the row, show a loading shimmer
//   failed  → terminal error, show a dimmed-placeholder distinct from
//             "still working"
//   no_preview → format had no embeddable preview (e.g. some video or
//             unrenderable RAW); show a neutral placeholder
export type ThumbStatus =
  | "ready"
  | "pending"
  | "working"
  | "failed"
  | "no_preview";

export type MediaFile = {
  id: string;
  role: string;
  mime_type: string;
  original_filename: string;
  size: number;
  sha256: string;
};

export type Media = {
  id: string;
  timestamp: string;
  aspect: number;
  thumbUrl: string;
  thumbStatus: ThumbStatus;
  taken: Date;
  thumbVersion: number;
  latitude?: number;
  longitude?: number;
  gps_at?: string;
  location_label?: string;
  original_filename?: string;
  size?: number;
  files?: MediaFile[];
  // F2.4 hidden privacy. Non-null means the row is hidden. Hidden rows
  // must NOT appear in visible MediaStore — they live only in
  // HiddenMediaStore (Task 12). This field is present on the type so
  // toMedia can parse it; once set, the row is routed out of visible
  // indexes immediately.
  hidden_at?: string | null;
};

export type Month = {
  key: string; // YYYY-MM
  items: Media[];
};

export function monthKey(d: Date): string {
  const y = d.getUTCFullYear();
  const m = String(d.getUTCMonth() + 1).padStart(2, "0");
  return `${y}-${m}`;
}

type MediaHiddenPayload = { ids: string[]; hiddenAt: string };
type MediaStoreEventMap = {
  "media:hidden": MediaHiddenPayload;
};

export class MediaStore {
  months = $state<Month[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  private nextOffset: number | null = 0;
  // Active loadMore() promise. Re-entrant callers (e.g. the Lightbox
  // reconstruction loop racing the route's initial load) return this so
  // `await loadMore()` waits for the in-flight load to settle instead of
  // resolving immediately and burning the caller's retry cap.
  private inflight: Promise<void> | null = null;

  // byMonth: monthKey → (id → Media). Long-lived; survives across
  // merge() calls. byId: id → current monthKey, used to relocate a
  // row when its timestamp moves across months.
  private byMonth = new Map<string, Map<string, Media>>();
  private byId = new Map<string, string>();
  private byMediaId = new Map<string, Media>();

  // Event listeners for media:hidden. Simple pub/sub — no external
  // bus dependency. Subscribers (AlbumStore, toasts) call on/off.
  private hiddenListeners = new Set<(p: MediaHiddenPayload) => void>();

  // Active filter set + reset protocol (SF-11). When the filterKey
  // changes, the store bumps fetchToken, clears every visible index,
  // and resets pagination so the next loadMore restarts at offset 0.
  // In-flight loadMore calls capture the token at issue time and
  // discard their response if the token has moved on — this is what
  // prevents stale rows from interleaving with the new filter set.
  private filters: ActiveFilters = {
    cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
  };
  private currentFilterKey = filterKey(this.filters);
  private fetchToken = 0;

  constructor(private client: Pick<Client, "listMedia">) {}

  /**
   * Update the active filters. If the filterKey changes, the store
   * resets pagination, clears all cached rows, drops any in-flight
   * response, and bumps fetchToken so late completions are dropped.
   * Callers should re-call loadInitial() (or loadMore()) afterwards.
   *
   * If the filterKey is unchanged this is a no-op — protects against
   * router replays that rebuild ActiveFilters from URL params on every
   * navigation but don't actually change the filter set.
   */
  setFilters(next: ActiveFilters): void {
    const nextKey = filterKey(next);
    if (nextKey === this.currentFilterKey) return;
    this.filters = next;
    this.currentFilterKey = nextKey;
    this.fetchToken++;
    this.byMonth.clear();
    this.byId.clear();
    this.byMediaId.clear();
    this.months = [];
    this.loadError = false;
    this.exhausted = false;
    this.nextOffset = 0;
    this.inflight = null;
    this.loading = false;
  }

  get(id: string): Media | undefined {
    return this.byMediaId.get(id);
  }

  /** Subscribe to a store event. Currently only "media:hidden" is emitted. */
  on<K extends keyof MediaStoreEventMap>(
    event: K,
    handler: (payload: MediaStoreEventMap[K]) => void,
  ): void {
    if (event === "media:hidden") {
      this.hiddenListeners.add(handler as (p: MediaHiddenPayload) => void);
    }
  }

  /** Unsubscribe a previously registered handler. */
  off<K extends keyof MediaStoreEventMap>(
    event: K,
    handler: (payload: MediaStoreEventMap[K]) => void,
  ): void {
    if (event === "media:hidden") {
      this.hiddenListeners.delete(handler as (p: MediaHiddenPayload) => void);
    }
  }

  async loadInitial() { await this.loadMore(); }

  loadError = $state(false);

  retry(): Promise<void> {
    this.loadError = false;
    return this.loadMore();
  }

  loadMore(): Promise<void> {
    if (this.exhausted || this.loadError) return Promise.resolve();
    // Re-entry: hand back the in-flight promise so `await loadMore()` only
    // resolves once the original load completes. Returning `Promise.resolve()`
    // here would let the caller's loop spin against `loading=true` and
    // exhaust its attempt cap before the network even returns.
    if (this.inflight !== null) return this.inflight;
    // Capture the token at issue time so a setFilters that lands while
    // this fetch is in flight can mark our response stale. Without this
    // a slow GET could overwrite the cleared store with rows for the
    // PREVIOUS filter set after the new fetch has already settled.
    const myToken = this.fetchToken;
    this.loading = true;
    const p = (async () => {
      try {
        const res = await this.client.listMedia(this.buildQuery());
        // Stale: a setFilters during the await invalidated us. Drop
        // every byte of this response so it cannot leak into the
        // post-reset store.
        if (myToken !== this.fetchToken) return;
        if (res.error || !res.data) {
          this.loadError = true;
          return;
        }
        const items = ((res.data as unknown as { items?: Array<Record<string, unknown>> }).items ?? [])
          .map(toMedia)
          .filter((m): m is Media => m !== null);
        this.merge(items);
        const next = (res.data as { next_offset?: number | null }).next_offset ?? null;
        this.nextOffset = next;
        if (next === null) this.exhausted = true;
      } catch {
        if (myToken === this.fetchToken) this.loadError = true;
      } finally {
        // Only the latest fetch owns the loading flag and the inflight
        // slot; a stale completion must not clobber state owned by the
        // newer fetch (e.g. clearing loading=true while the new fetch
        // is still pending).
        if (myToken === this.fetchToken) {
          this.loading = false;
          this.inflight = null;
        }
      }
    })();
    this.inflight = p;
    return p;
  }

  /**
   * Build the query object for the /api/v1/media GET. Filter params
   * (camera/lens/facet_tag/has_gps/media_type) ride alongside the
   * pagination/sort params; SF-17 will start honouring them server-side.
   */
  private buildQuery(): Record<string, unknown> {
    const q: Record<string, unknown> = {
      limit: 200,
      offset: this.nextOffset ?? 0,
      sort_desc: true,
    };
    if (this.filters.cameras.length > 0) q["camera"] = [...this.filters.cameras];
    if (this.filters.lenses.length > 0) q["lens"] = [...this.filters.lenses];
    if (this.filters.tagKeys.length > 0) q["facet_tag"] = [...this.filters.tagKeys];
    // huma's *bool query binding accepts the literal "true"/"false"
    // strings. Match facetsStore's wire format (SF-10) so the moment
    // SF-17 lands the backend will accept what we send unchanged.
    if (this.filters.hasGps !== null) q["has_gps"] = this.filters.hasGps ? "true" : "false";
    if (this.filters.mediaType !== null) q["media_type"] = this.filters.mediaType;
    return q;
  }

  // Public adapter for the on-miss fetch path (e.g. MediaDetail loads
  // /api/v1/media/{id} when the row isn't already in the store). Mirrors
  // the loadMore pipeline: filter to objects, run the toMedia adapter,
  // drop nulls, then merge.
  //
  // Hidden rows (hidden_at != null) are skipped entirely. If a
  // previously-visible row comes back hidden, it is evicted from all
  // visible indexes — it must be refetched via HiddenMediaStore (Task 12).
  mergeRaw(rawItems: unknown[]): void {
    const items = rawItems
      .filter((r): r is Record<string, unknown> => typeof r === "object" && r !== null)
      .map(toMedia)
      .filter((m): m is Media => m !== null);
    this.merge(items);
  }

  /**
   * Evict ids from all visible indexes and emit a "media:hidden" event.
   *
   * Called by the Hide/Unhide action flow (Task 13) after the API
   * confirms the rows are hidden. hiddenAt defaults to now().
   */
  removeMany(ids: string[], hiddenAt: string = new Date().toISOString()): void {
    for (const id of ids) {
      this.removeFromVisibleIndexes(id);
    }
    const payload: MediaHiddenPayload = { ids, hiddenAt };
    for (const handler of this.hiddenListeners) {
      handler(payload);
    }
  }

  /**
   * Remove a single id from all four visible indexes: byMonth, byId,
   * byMediaId, and the months reactive snapshot. Prunes empty month
   * buckets and rebuilds the months array.
   */
  private removeFromVisibleIndexes(id: string): void {
    const monthKey = this.byId.get(id);
    if (monthKey === undefined) return;

    const inner = this.byMonth.get(monthKey);
    if (inner) {
      inner.delete(id);
      if (inner.size === 0) this.byMonth.delete(monthKey);
    }
    this.byId.delete(id);
    this.byMediaId.delete(id);

    // Rebuild the reactive months snapshot.
    const prev = new Map(this.months.map((m) => [m.key, m]));
    const sortedKeys = Array.from(this.byMonth.keys()).sort((a, b) =>
      a < b ? 1 : a > b ? -1 : 0,
    );
    this.months = sortedKeys.map((k) => {
      const prevMonth = prev.get(k);
      // If this month lost the evicted row its inner map is now
      // smaller; rebuild it. Other months are untouched.
      if (prevMonth && k !== monthKey) return prevMonth;
      const innerMap = this.byMonth.get(k)!;
      const sorted = Array.from(innerMap.values()).sort((a, b) => +b.taken - +a.taken);
      return { key: k, items: sorted };
    });
  }

  private merge(items: Media[]) {
    // Build-time guard: this distributed-conditional fails to compile
    // if Media gains a field outside the set checked by `unchanged`
    // below. Without it, a future field (e.g. caption, tags) could
    // silently bypass dirty-tracking — `inner.set` is gated on
    // !unchanged, so the bucket would keep stale values forever.
    // Update both this list AND the predicate when Media changes.
    type _IdentityFieldsCovered = Exclude<
      keyof Media,
      | "id" | "timestamp" | "taken" | "aspect" | "thumbUrl"
      | "thumbStatus" | "thumbVersion"
      | "latitude" | "longitude" | "gps_at" | "location_label"
      | "original_filename" | "size" | "files"
      | "hidden_at"
    >;
    type _AssertNoUncoveredFields = _IdentityFieldsCovered extends never ? true : never;
    const _identityFieldsCovered: _AssertNoUncoveredFields = true;
    void _identityFieldsCovered;

    // Track which month buckets changed so we can rebuild only those
    // entries in the months snapshot. Untouched months reuse their
    // existing object ref → VirtualGrid's keyed each-block skips
    // re-renders for them. A re-merge of an identical row is a no-op:
    // we compare identity fields and skip the dirty mark when they
    // match, which is what lets the SSE-overlap and refetch paths run
    // without churning every chunk.
    const dirty = new Set<string>();
    for (const incoming of items) {
      const known = this.byMediaId.get(incoming.id);
      const it = incoming.files === undefined && known?.files !== undefined
        ? { ...incoming, files: known.files }
        : incoming;
      // Visible-only invariant (§3.14): hidden rows must not enter any
      // visible index. If a previously-cached row comes back hidden,
      // evict it. After eviction the caller must use HiddenMediaStore.
      if (it.hidden_at != null) {
        if (this.byId.has(it.id)) {
          this.removeFromVisibleIndexes(it.id);
        }
        continue;
      }

      const newKey = monthKey(it.taken);
      const oldKey = this.byId.get(it.id);
      if (oldKey !== undefined && oldKey !== newKey) {
        // Cross-month relocation: remove from the old bucket and
        // prune if the bucket emptied.
        const oldInner = this.byMonth.get(oldKey);
        if (oldInner) {
          oldInner.delete(it.id);
          if (oldInner.size === 0) this.byMonth.delete(oldKey);
        }
        dirty.add(oldKey);
      }
      let inner = this.byMonth.get(newKey);
      if (!inner) {
        inner = new Map<string, Media>();
        this.byMonth.set(newKey, inner);
        dirty.add(newKey); // brand-new month, must be in the snapshot
      }
      const existing = inner.get(it.id);
      const unchanged = existing !== undefined
        && existing.timestamp === it.timestamp
        && existing.thumbUrl === it.thumbUrl
        && existing.thumbStatus === it.thumbStatus
        && existing.aspect === it.aspect
        && existing.thumbVersion === it.thumbVersion
        && existing.latitude === it.latitude
        && existing.longitude === it.longitude
        && existing.gps_at === it.gps_at
        && existing.location_label === it.location_label
        && existing.original_filename === it.original_filename
        && existing.size === it.size
        && filesShallowEqual(existing.files, it.files)
        && (existing.hidden_at ?? null) === (it.hidden_at ?? null);
      if (!unchanged) {
        inner.set(it.id, it);
        dirty.add(newKey);
      }
      // byId always reflects the latest known location for this id.
      this.byId.set(it.id, newKey);
      this.byMediaId.set(it.id, it);
    }

    // Rebuild the reactive months snapshot. Clean months reuse the
    // existing object ref; dirty (or new) months get a fresh object
    // with re-sorted items.
    const prev = new Map(this.months.map((m) => [m.key, m]));
    const sortedKeys = Array.from(this.byMonth.keys()).sort((a, b) =>
      a < b ? 1 : a > b ? -1 : 0,
    );
    this.months = sortedKeys.map((k) => {
      if (!dirty.has(k)) {
        const reuse = prev.get(k);
        if (reuse) return reuse;
      }
      const inner = this.byMonth.get(k)!;
      const sorted = Array.from(inner.values()).sort((a, b) => +b.taken - +a.taken);
      return { key: k, items: sorted };
    });
  }
}

function filesShallowEqual(a?: MediaFile[], b?: MediaFile[]): boolean {
  if (!a && !b) return true;
  if (!a || !b) return false;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    const x = a[i];
    const y = b[i];
    if (!x || !y) return false;
    if (
      x.id !== y.id
      || x.role !== y.role
      || x.mime_type !== y.mime_type
      || x.original_filename !== y.original_filename
      || x.size !== y.size
      || x.sha256 !== y.sha256
    ) return false;
  }
  return true;
}

export function toMedia(raw: Record<string, unknown>): Media | null {
  const id = raw["id"];
  const ts = typeof raw["timestamp"] === "string" ? raw["timestamp"] : raw["imported_at"];
  const w = raw["width"];
  const h = raw["height"];
  if (typeof id !== "string" || typeof ts !== "string") return null;
  const taken = new Date(ts);
  if (isNaN(+taken)) return null;
  const wn = typeof w === "number" && Number.isFinite(w) && w > 0 ? w : 1;
  const hn = typeof h === "number" && Number.isFinite(h) && h > 0 ? h : 1;
  // The thumb endpoint requires a non-negative integer ?v= matching
  // the row's thumb_version; without it the handler returns 404 (see
  // internal/httpapi/media_thumb.go). Fall back to 0 when the field is
  // missing or invalid — backend will 404, which surfaces the data
  // gap instead of silently rendering nothing on a "good" URL.
  const tv = raw["thumb_version"];
  const thumbVersion = typeof tv === "number" && Number.isFinite(tv) && tv >= 0 ? tv : 0;
  // thumb_status guards rendering: pending/working should show a
  // shimmer instead of a 404'd <img>. Default to "pending" when the
  // backend omits the field — that's the safer default since a 404
  // would surface as a "broken" state for a row the worker simply
  // hasn't reached yet.
  const tsRaw = raw["thumb_status"];
  let thumbStatus: ThumbStatus = "pending";
  if (
    tsRaw === "ready" || tsRaw === "pending" || tsRaw === "working"
    || tsRaw === "failed" || tsRaw === "no_preview"
  ) {
    thumbStatus = tsRaw;
  }
  // tsconfig has exactOptionalPropertyTypes:true, so the optional fields
  // (declared as `?: T`) reject explicit `undefined`. Build the literal
  // and only assign each optional when its raw value passes a typeof
  // check; missing/wrong-type input ⇒ property simply absent.
  const m: Media = {
    id,
    timestamp: ts,
    taken,
    aspect: wn / hn,
    thumbUrl: `/api/v1/media/${id}/thumb?size=grid&v=${thumbVersion}`,
    thumbStatus,
    thumbVersion,
  };
  if (typeof raw["latitude"] === "number") m.latitude = raw["latitude"];
  if (typeof raw["longitude"] === "number") m.longitude = raw["longitude"];
  if (typeof raw["gps_at"] === "string") m.gps_at = raw["gps_at"];
  if (typeof raw["location_label"] === "string") m.location_label = raw["location_label"];
  if (typeof raw["original_filename"] === "string") m.original_filename = raw["original_filename"];
  if (typeof raw["size"] === "number" && Number.isFinite(raw["size"])) m.size = raw["size"];
  const rawFiles = raw["files"];
  if (Array.isArray(rawFiles)) {
    const files: MediaFile[] = [];
    for (const rawFile of rawFiles) {
      if (rawFile === null || typeof rawFile !== "object") continue;
      const file = rawFile as Record<string, unknown>;
      if (
        typeof file["id"] === "string"
        && typeof file["role"] === "string"
        && typeof file["mime_type"] === "string"
        && typeof file["original_filename"] === "string"
        && typeof file["size"] === "number"
        && Number.isFinite(file["size"])
        && typeof file["sha256"] === "string"
      ) {
        files.push({
          id: file["id"],
          role: file["role"],
          mime_type: file["mime_type"],
          original_filename: file["original_filename"],
          size: file["size"],
          sha256: file["sha256"],
        });
      }
    }
    m.files = files;
  }
  // F2.4: hidden_at — string (ISO timestamp) or null from the backend.
  // null means "was hidden but is now visible again" (unhide flow).
  // undefined means the field wasn't present (treat as visible).
  const ha = raw["hidden_at"];
  if (typeof ha === "string") m.hidden_at = ha;
  else if (ha === null) m.hidden_at = null;
  return m;
}

import type { Client } from "../api/client";

export type Media = {
  id: string;
  timestamp: string;
  aspect: number;
  thumbUrl: string;
  taken: Date;
  thumbVersion: number;
  latitude?: number;
  longitude?: number;
  gps_at?: string;
  location_label?: string;
  // F2.2 RAW + JPEG pairing. original_filename and size are surfaced
  // in MediaDetail's primary Files row and sidecar direct page; both
  // are populated by the backend MediaDTO on every response.
  original_filename?: string;
  size?: number;
  paired_with_id?: string;
  paired_with?: { id: string; original_filename: string };
  sidecars?: Media[];
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

  // byMonth: monthKey → (id → Media). Long-lived; survives across
  // merge() calls. byId: id → current monthKey, used to relocate a
  // row when its timestamp moves across months.
  private byMonth = new Map<string, Map<string, Media>>();
  private byId = new Map<string, string>();
  private byMediaId = new Map<string, Media>();

  // Event listeners for media:hidden. Simple pub/sub — no external
  // bus dependency. Subscribers (AlbumStore, toasts) call on/off.
  private hiddenListeners = new Set<(p: MediaHiddenPayload) => void>();

  constructor(private client: Pick<Client, "GET">) {}

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

  async loadMore() {
    if (this.loading || this.exhausted) return;
    // Synchronous before any await — required as the re-entry guard.
    this.loading = true;
    try {
      const res = await this.client.GET("/api/v1/media", {
        // sort_desc: true so the library opens at the most-recent
        // capture (the backend defaults to ascending). Pagination then
        // walks backwards in time as the user scrolls down.
        params: {
          query: { limit: 200, offset: this.nextOffset ?? 0, sort_desc: true },
        } as never,
      });
      if (res.error || !res.data) return;
      const items = ((res.data as { items?: Array<Record<string, unknown>> }).items ?? [])
        .map(toMedia)
        .filter((m): m is Media => m !== null);
      this.merge(items);
      const next = (res.data as { next_offset?: number | null }).next_offset ?? null;
      this.nextOffset = next;
      if (next === null) this.exhausted = true;
    } finally {
      this.loading = false;
    }
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
      | "thumbVersion" | "latitude" | "longitude" | "gps_at" | "location_label"
      | "original_filename" | "size"
      | "paired_with_id" | "paired_with" | "sidecars"
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
    for (const it of items) {
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
        && existing.aspect === it.aspect
        && existing.thumbVersion === it.thumbVersion
        && existing.latitude === it.latitude
        && existing.longitude === it.longitude
        && existing.gps_at === it.gps_at
        && existing.location_label === it.location_label
        && existing.original_filename === it.original_filename
        && existing.size === it.size
        && (existing.paired_with_id ?? null) === (it.paired_with_id ?? null)
        && (existing.paired_with?.id ?? null) === (it.paired_with?.id ?? null)
        && (existing.paired_with?.original_filename ?? null) === (it.paired_with?.original_filename ?? null)
        && sidecarsShallowEqual(existing.sidecars, it.sidecars)
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

// sidecarsShallowEqual compares two sidecar lists as ordered sequences,
// matching on the fields the UI actually consumes (id and
// original_filename today). Backend returns sidecars sorted by
// (original_filename, id) — see media.Repo.GetSidecars; reordering
// across two responses for the same primary would falsely dirty the
// bucket on every poll. If a future UI change starts rendering more
// sidecar fields (thumb_status, thumb_version, …), extend this
// comparison so renames/version-bumps still propagate.
function sidecarsShallowEqual(a?: Media[], b?: Media[]): boolean {
  if (!a && !b) return true;
  if (!a || !b) return false;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    const x = a[i];
    const y = b[i];
    if (!x || !y) return false;
    if (x.id !== y.id) return false;
    if ((x.original_filename ?? null) !== (y.original_filename ?? null)) return false;
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
    thumbVersion,
  };
  if (typeof raw["latitude"] === "number") m.latitude = raw["latitude"];
  if (typeof raw["longitude"] === "number") m.longitude = raw["longitude"];
  if (typeof raw["gps_at"] === "string") m.gps_at = raw["gps_at"];
  if (typeof raw["location_label"] === "string") m.location_label = raw["location_label"];
  if (typeof raw["original_filename"] === "string") m.original_filename = raw["original_filename"];
  if (typeof raw["size"] === "number" && Number.isFinite(raw["size"])) m.size = raw["size"];
  if (typeof raw["paired_with_id"] === "string") m.paired_with_id = raw["paired_with_id"];
  const pw = raw["paired_with"];
  if (pw !== null && typeof pw === "object") {
    const pwObj = pw as Record<string, unknown>;
    const pwId = pwObj["id"];
    const pwName = pwObj["original_filename"];
    if (typeof pwId === "string" && typeof pwName === "string") {
      m.paired_with = { id: pwId, original_filename: pwName };
    }
  }
  const sc = raw["sidecars"];
  if (Array.isArray(sc) && sc.length > 0) {
    const mapped = sc
      .filter((r): r is Record<string, unknown> => typeof r === "object" && r !== null)
      .map((r) => {
        // Strip nested sidecars: backend contract guarantees a
        // sidecar's own Sidecars is empty; stripping defensively
        // ensures toMedia is self-correcting against a future leak
        // since sidecarsShallowEqual only inspects the top-level array.
        const { sidecars: _ignoredNestedSidecars, ...rest } = r;
        return toMedia(rest);
      })
      .filter((x): x is Media => x !== null)
      // Filter out hidden sidecars: a hidden sidecar must not appear in
      // visible media's file list — it lives only in HiddenMediaStore.
      .filter((x) => x.hidden_at == null);
    if (mapped.length > 0) m.sidecars = mapped;
  }
  // F2.4: hidden_at — string (ISO timestamp) or null from the backend.
  // null means "was hidden but is now visible again" (unhide flow).
  // undefined means the field wasn't present (treat as visible).
  const ha = raw["hidden_at"];
  if (typeof ha === "string") m.hidden_at = ha;
  else if (ha === null) m.hidden_at = null;
  return m;
}

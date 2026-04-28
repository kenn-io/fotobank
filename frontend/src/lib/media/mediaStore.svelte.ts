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

  constructor(private client: Pick<Client, "GET">) {}

  get(id: string): Media | undefined {
    return this.byMediaId.get(id);
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
  mergeRaw(rawItems: unknown[]): void {
    const items = rawItems
      .filter((r): r is Record<string, unknown> => typeof r === "object" && r !== null)
      .map(toMedia)
      .filter((m): m is Media => m !== null);
    this.merge(items);
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
        && existing.location_label === it.location_label;
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

function toMedia(raw: Record<string, unknown>): Media | null {
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
  // tsconfig has exactOptionalPropertyTypes:true, so the four GPS fields
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
  return m;
}

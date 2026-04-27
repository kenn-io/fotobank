import type { Client } from "../api/client";

export type Media = {
  id: string;
  timestamp: string;
  aspect: number;
  thumbUrl: string;
  taken: Date;
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

  constructor(private client: Pick<Client, "GET">) {}

  async loadInitial() { await this.loadMore(); }

  async loadMore() {
    if (this.loading || this.exhausted) return;
    // Synchronous before any await — required as the re-entry guard.
    this.loading = true;
    try {
      const res = await this.client.GET("/api/v1/media", {
        params: { query: { limit: 200, offset: this.nextOffset ?? 0 } } as never,
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

  private merge(items: Media[]) {
    const byMonth = new Map<string, Map<string, Media>>();
    for (const m of this.months) {
      const inner = new Map<string, Media>();
      for (const it of m.items) inner.set(it.id, it);
      byMonth.set(m.key, inner);
    }
    for (const it of items) {
      const k = monthKey(it.taken);
      const inner = byMonth.get(k) ?? new Map<string, Media>();
      inner.set(it.id, it);
      byMonth.set(k, inner);
    }
    this.months = Array.from(byMonth.entries())
      .map(([key, inner]) => ({
        key,
        items: Array.from(inner.values()).sort((a, b) => +b.taken - +a.taken),
      }))
      .sort((a, b) => (a.key < b.key ? 1 : -1));
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
  return {
    id,
    timestamp: ts,
    taken,
    aspect: wn / hn,
    thumbUrl: `/api/v1/media/${id}/thumb`,
  };
}

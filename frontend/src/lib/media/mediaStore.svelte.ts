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
    const byMonth = new Map<string, Media[]>();
    for (const m of this.months) byMonth.set(m.key, m.items.slice());
    for (const it of items) {
      const k = monthKey(it.taken);
      const list = byMonth.get(k) ?? [];
      list.push(it);
      byMonth.set(k, list);
    }
    this.months = Array.from(byMonth.entries())
      .map(([key, items]) => ({
        key,
        items: items.sort((a, b) => +b.taken - +a.taken),
      }))
      .sort((a, b) => (a.key < b.key ? 1 : -1));
  }
}

function toMedia(raw: Record<string, unknown>): Media | null {
  const id = raw["id"];
  const ts = raw["timestamp"];
  const w = raw["width"];
  const h = raw["height"];
  if (typeof id !== "string" || typeof ts !== "string") return null;
  const taken = new Date(ts);
  if (isNaN(+taken)) return null;
  const wn = typeof w === "number" ? w : 1;
  const hn = typeof h === "number" ? h : 1;
  return {
    id,
    timestamp: ts,
    taken,
    aspect: hn === 0 ? 1 : wn / hn,
    thumbUrl: `/api/v1/media/${id}/thumb`,
  };
}

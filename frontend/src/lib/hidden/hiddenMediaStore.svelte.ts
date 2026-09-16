// frontend/src/lib/hidden/hiddenMediaStore.svelte.ts
//
// Route-scoped store for hidden media. Mirrors MediaStore's paginated
// shape but loads from GET /api/v1/hidden/media and never touches
// the shared MediaStore. Constructed inside HiddenLibrary.svelte and
// torn down when the route unmounts (via GC of the route component).

import type { Client } from "../api/client";
import { type Media, type Month, toMedia, monthKey } from "../media/mediaStore.svelte";

export type { Media, Month };

export class HiddenMediaStore {
  months = $state<Month[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  // loadError surfaces the HTTP status code of the last failed fetch, or
  // null when no error has occurred. 403 means the session expired and the
  // caller should re-show the gate (finding #6).
  loadError = $state<number | null>(null);

  private nextOffset: number | null = 0;
  private byMonth = new Map<string, Map<string, Media>>();
  private byId = new Map<string, string>();

  constructor(private client: Pick<Client, "listHiddenMedia">) {}

  async loadInitial(): Promise<void> {
    await this.loadMore();
  }

  async loadMore(): Promise<void> {
    if (this.loading || this.exhausted) return;
    this.loading = true;
    try {
      const res = await this.client.listHiddenMedia({ limit: 200, offset: this.nextOffset ?? 0 });
      if (res.error || !res.data) {
        const status = (res.error as { status?: number } | undefined)?.status ?? 0;
        this.loadError = status;
        return;
      }
      this.loadError = null;
      const items = ((res.data as unknown as { items?: Array<Record<string, unknown>> }).items ?? [])
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

  /**
   * Evict ids from all local indexes. Called after unhide completes
   * (the rows are now visible again and no longer belong here).
   */
  removeMany(ids: string[]): void {
    for (const id of ids) {
      this.removeFromIndexes(id);
    }
    this.rebuildMonths();
  }

  /**
   * Reset the store back to its initial state. HiddenLibrary calls this
   * on a 403 lock so the next unlock starts with empty pagination,
   * cleared loadError, and a fresh fetch — without it the route would
   * keep its old months + loadError=403 and never refetch.
   */
  reset(): void {
    this.months = [];
    this.loading = false;
    this.exhausted = false;
    this.loadError = null;
    this.nextOffset = 0;
    this.byMonth = new Map();
    this.byId = new Map();
  }

  private removeFromIndexes(id: string): void {
    const mk = this.byId.get(id);
    if (mk === undefined) return;
    const inner = this.byMonth.get(mk);
    if (inner) {
      inner.delete(id);
      if (inner.size === 0) this.byMonth.delete(mk);
    }
    this.byId.delete(id);
  }

  private merge(items: Media[]): void {
    const dirty = new Set<string>();
    for (const it of items) {
      const mk = monthKey(it.taken);
      const old = this.byId.get(it.id);
      if (old !== undefined && old !== mk) {
        const oldInner = this.byMonth.get(old);
        if (oldInner) {
          oldInner.delete(it.id);
          if (oldInner.size === 0) this.byMonth.delete(old);
        }
        dirty.add(old);
      }
      let inner = this.byMonth.get(mk);
      if (!inner) {
        inner = new Map<string, Media>();
        this.byMonth.set(mk, inner);
        dirty.add(mk);
      }
      const existing = inner.get(it.id);
      if (!existing || existing.timestamp !== it.timestamp || existing.thumbUrl !== it.thumbUrl) {
        inner.set(it.id, it);
        dirty.add(mk);
      }
      this.byId.set(it.id, mk);
    }
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

  private rebuildMonths(): void {
    const sortedKeys = Array.from(this.byMonth.keys()).sort((a, b) =>
      a < b ? 1 : a > b ? -1 : 0,
    );
    this.months = sortedKeys.map((k) => {
      const inner = this.byMonth.get(k)!;
      const sorted = Array.from(inner.values()).sort((a, b) => +b.taken - +a.taken);
      return { key: k, items: sorted };
    });
  }
}

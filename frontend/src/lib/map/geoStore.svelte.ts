// frontend/src/lib/map/geoStore.svelte.ts
//
// Cache of GET /api/v1/media/geo, populated on /map mount and on
// explicit reload (e.g. when the user toggles "Include hidden" on
// while unlocked). Also serves the LightboxMapPin's focus-retry path.
//
// Boundary: GeoStore does NOT validate the unlock claim — the backend
// handler does (B3). The store just threads include_hidden through and
// records the flag it fetched under, so callers (Lightbox, MapPane)
// know whether the cached set is hidden-aware.

import type { Client } from "../api/client";
import { toMedia, type Media } from "../media/mediaStore.svelte";

export class GeoStore {
  private _items = $state<Media[]>([]);
  private _ready = $state(false);
  private _error = $state<string | null>(null);
  private _includedHidden = $state(false);

  constructor(private client: Pick<Client, "GET">) {}

  get items(): Media[] {
    return this._items;
  }
  get ready(): boolean {
    return this._ready;
  }
  get error(): string | null {
    return this._error;
  }
  get includedHiddenAtFetch(): boolean {
    return this._includedHidden;
  }

  async load(includeHidden: boolean): Promise<void> {
    this._error = null;
    try {
      const opts: { params?: { query: { include_hidden: true } } } = {};
      if (includeHidden) opts.params = { query: { include_hidden: true } };
      const { data, error } = await this.client.GET("/api/v1/media/geo", opts);
      if (error || !data) {
        this._error = "geo fetch failed";
        return;
      }
      const parsed: Media[] = [];
      for (const raw of data.items ?? []) {
        const m = toMedia(raw as unknown as Record<string, unknown>);
        if (m) parsed.push(m);
      }
      this._items = parsed;
      this._includedHidden = includeHidden;
      this._ready = true;
    } catch (e) {
      this._error = e instanceof Error ? e.message : "network error";
    }
  }

  findById(id: string): Media | undefined {
    return this._items.find((m) => m.id === id);
  }
}

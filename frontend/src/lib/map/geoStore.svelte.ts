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

// GeoLoadOptions narrows the geotagged set the same way ActiveFilters
// narrows /api/v1/media on /library and /search. has_gps is
// intentionally absent — /geo is geotagged-only by contract. Empty
// arrays / null are treated as "no filter" by load() so callers can
// pass the empty filter shape unconditionally.
export type GeoLoadOptions = {
  cameras?: string[];
  lenses?: string[];
  facetTags?: string[];
  mediaType?: "photo" | "video" | null;
};

export class GeoStore {
  private _items = $state<Media[]>([]);
  // _rawItems retains the unparsed server payload so callers (Map.svelte)
  // can feed the same rows into MediaStore.mergeRaw without re-fetching.
  // Without this, a /map photo whose row hasn't yet been paged into the
  // visible MediaStore would render blank in MapGridPane.
  private _rawItems = $state<unknown[]>([]);
  private _ready = $state(false);
  private _error = $state<string | null>(null);
  private _includedHidden = $state(false);
  // _filterKey records the filter narrowing the cache was fetched
  // under, alongside _includedHidden. It's compared in load() so a
  // narrowed re-fetch (e.g. user toggles a Camera chip) clears stale
  // items synchronously — same protocol as the include_hidden→false
  // transition, just keyed off the active filters as well.
  private _filterKey = $state<string>("");
  // Monotonic request token. Each load() increments this and only
  // applies its result if the token still matches at resolution time.
  // Without it, a slow include_hidden=false response landing AFTER a
  // newer include_hidden=true response would clobber the latest cache
  // and the includedHidden flag would lie about what's in items.
  private requestSeq = 0;

  constructor(private client: Pick<Client, "listMediaGeo">) {}

  get items(): Media[] {
    return this._items;
  }
  get rawItems(): unknown[] {
    return this._rawItems;
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

  async load(includeHidden: boolean, opts: GeoLoadOptions = {}): Promise<void> {
    const myReq = ++this.requestSeq;
    this._error = null;

    const cameras = opts.cameras ?? [];
    const lenses = opts.lenses ?? [];
    const facetTags = opts.facetTags ?? [];
    const mediaType = opts.mediaType ?? null;
    const nextKey = computeFilterKey({ includeHidden, cameras, lenses, facetTags, mediaType });

    // Narrowing the visible set (include_hidden=true → include_hidden=false,
    // OR any change to the camera/lens/tag/mediaType filters) must clear
    // stale items synchronously: otherwise the map would re-render the
    // previously fetched markers between the request kick-off and its
    // resolution. This bites the retry-after-error path too — the
    // error branch leaves the cached items in place, and a retry's
    // narrower request would briefly flash the old set.
    //
    // We treat any filter-key change as a narrowing event for the cache:
    // even an additive widening (e.g. unchecking a chip that was
    // previously narrowing) resets the displayed items so the user
    // never sees rows mixed across two distinct filter sets.
    if (this._ready && this._filterKey !== nextKey) {
      this._items = [];
      this._rawItems = [];
      this._ready = false;
      this._includedHidden = false;
    }
    try {
      const query: Record<string, unknown> = {};
      if (includeHidden) query["include_hidden"] = true;
      if (cameras.length > 0) query["camera"] = [...cameras];
      if (lenses.length > 0) query["lens"] = [...lenses];
      if (facetTags.length > 0) query["facet_tag"] = [...facetTags];
      if (mediaType !== null) query["media_type"] = mediaType;
      const { data, error } = await this.client.listMediaGeo(query);
      if (myReq !== this.requestSeq) return;
      if (error || !data) {
        this._error = "geo fetch failed";
        return;
      }
      const raws = data.items ?? [];
      const parsed: Media[] = [];
      for (const raw of raws) {
        const m = toMedia(raw as unknown as Record<string, unknown>);
        if (m) parsed.push(m);
      }
      this._items = parsed;
      this._rawItems = raws;
      this._includedHidden = includeHidden;
      this._filterKey = nextKey;
      this._ready = true;
    } catch (e) {
      if (myReq !== this.requestSeq) return;
      this._error = e instanceof Error ? e.message : "network error";
    }
  }

  findById(id: string): Media | undefined {
    return this._items.find((m) => m.id === id);
  }
}

function computeFilterKey(input: {
  includeHidden: boolean;
  cameras: string[];
  lenses: string[];
  facetTags: string[];
  mediaType: "photo" | "video" | null;
}): string {
  return JSON.stringify({
    includeHidden: input.includeHidden,
    cameras: [...input.cameras].sort(),
    lenses: [...input.lenses].sort(),
    facetTags: [...input.facetTags].sort(),
    mediaType: input.mediaType,
  });
}

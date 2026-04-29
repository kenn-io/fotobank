import type { Client } from "../api/client";
import type { MediaStore } from "../media/mediaStore.svelte";

export type Album = {
  id: string;
  name: string;
  created_at: string;
  updated_at: string;
  item_count: number;
};

export type AlbumSort = "taken" | "added";

export class AlbumDetailStore {
  album = $state<Album | null>(null);
  itemIds = $state<string[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  sort = $state<AlbumSort>("taken");
  // metaLoading is true while the metadata fetch in load() is in flight.
  // metaError is true when that fetch failed and no album was returned.
  // The route renders a loading or "not found" state from these so the
  // user never sees a blank page after a failed metadata fetch.
  metaLoading = $state(false);
  metaError = $state(false);

  private nextOffset: number | null = 0;
  private albumId: string | null = null;
  private membership = new Set<string>();
  // Monotonic token bumped on every load() / setSort(). Async fetches
  // capture the token at entry and check it before mutating state, so a
  // late response from a stale album/sort can't clobber a fresher load.
  private loadToken = 0;

  constructor(
    private client: Pick<Client, "GET" | "DELETE" | "PATCH">,
    private media: MediaStore,
  ) {}

  async load(id: string): Promise<void> {
    const token = ++this.loadToken;
    this.albumId = id;
    this.album = null;
    this.itemIds = [];
    this.membership = new Set();
    this.nextOffset = 0;
    this.exhausted = false;
    // Reset loading so a new loadMore can fetch even if a stale one is
    // still resolving — its response will be dropped by the token check.
    this.loading = false;
    this.metaError = false;
    this.metaLoading = true;

    let meta;
    try {
      meta = await this.client.GET("/api/v1/albums/{id}", {
        params: { path: { id } } as never,
      });
    } finally {
      // Only clear metaLoading if we're still the active token.
      if (token === this.loadToken) this.metaLoading = false;
    }
    if (token !== this.loadToken) return;
    if (meta.error || !meta.data) {
      this.metaError = true;
      return;
    }
    const a = meta.data as Album;
    this.album = {
      id: a.id,
      name: a.name,
      created_at: a.created_at,
      updated_at: a.updated_at,
      item_count: a.item_count,
    };

    await this.loadMore();
  }

  async loadMore(): Promise<void> {
    if (!this.albumId || this.loading || this.exhausted) return;
    const token = this.loadToken;
    this.loading = true;
    try {
      const res = await this.client.GET("/api/v1/albums/{id}/media", {
        params: {
          path: { id: this.albumId },
          query: {
            limit: 200,
            offset: this.nextOffset ?? 0,
            sort_by: this.sort,
            sort_asc: false,
          },
        } as never,
      });
      if (token !== this.loadToken) return;
      if (res.error || !res.data) return;
      const data = res.data as { items?: Array<Record<string, unknown>>; next_offset?: number | null };
      const items = data.items ?? [];
      this.media.mergeRaw(items);
      const newIds = items
        .map((it) => it["id"])
        .filter((v): v is string => typeof v === "string");
      this.itemIds = [...this.itemIds, ...newIds];
      for (const id of newIds) this.membership.add(id);
      const next = data.next_offset ?? null;
      this.nextOffset = next;
      if (next === null) this.exhausted = true;
    } finally {
      // Only clear loading if we're still the active token; otherwise a
      // newer load/setSort owns the flag and we shouldn't reset it.
      if (token === this.loadToken) this.loading = false;
    }
  }

  async setSort(next: AlbumSort): Promise<void> {
    if (this.sort === next || !this.albumId) return;
    this.sort = next;
    this.itemIds = [];
    this.membership = new Set();
    this.nextOffset = 0;
    this.exhausted = false;
    this.loading = false;
    // Bump the token so any in-flight loadMore for the prior sort drops
    // its response instead of polluting the new sort's pages.
    ++this.loadToken;
    await this.loadMore();
  }

  async removeMany(ids: string[]): Promise<{ succeeded: string[]; failed: string[] }> {
    if (!this.albumId) return { succeeded: [], failed: [] };
    const albumId = this.albumId;
    const concurrency = 4;
    const succeeded: string[] = [];
    const failed: string[] = [];
    let i = 0;
    const client = this.client;
    async function worker() {
      while (i < ids.length) {
        const myIdx = i++;
        const mediaId = ids[myIdx]!;
        const res = await client.DELETE("/api/v1/albums/{id}/media/{media_id}", {
          params: { path: { id: albumId, media_id: mediaId } } as never,
        });
        if (res.error) failed.push(mediaId);
        else succeeded.push(mediaId);
      }
    }
    await Promise.all(
      Array.from({ length: Math.min(concurrency, ids.length) }, () => worker()),
    );
    if (succeeded.length > 0) {
      const succSet = new Set(succeeded);
      this.itemIds = this.itemIds.filter((id) => !succSet.has(id));
      for (const id of succeeded) this.membership.delete(id);
    }
    return { succeeded, failed };
  }

  hasInAlbum(id: string): boolean {
    return this.membership.has(id);
  }

  async rename(name: string): Promise<void> {
    if (!this.albumId) return;
    const trimmed = name.trim();
    if (trimmed.length === 0) throw new Error("Name is required");
    if (trimmed.length > 200) throw new Error("Name exceeds 200 characters");
    const res = await this.client.PATCH("/api/v1/albums/{id}", {
      params: { path: { id: this.albumId } } as never,
      body: { name: trimmed } as never,
    });
    if (res.error) throw res.error;
    if (res.data && this.album) {
      const a = res.data as Album;
      this.album = { ...this.album, name: a.name, updated_at: a.updated_at };
    }
  }

  async delete(): Promise<void> {
    if (!this.albumId) return;
    const res = await this.client.DELETE("/api/v1/albums/{id}", {
      params: { path: { id: this.albumId } } as never,
    });
    if (res.error) throw res.error;
  }
}

import type { Client } from "../api/client";

export type AlbumListItem = {
  id: string;
  name: string;
  created_at: string;
  updated_at: string;
  item_count: number;
  cover?: { media_id: string; thumb_version: number };
};

export class AlbumsStore {
  albums = $state<AlbumListItem[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  private nextOffset: number | null = 0;
  private inflight: Promise<void> | null = null;

  constructor(private client: Pick<Client, "GET" | "POST" | "PATCH" | "DELETE">) {}

  async loadInitial(): Promise<void> {
    // Wait for any in-flight loadMore to settle so resetting state and
    // re-fetching doesn't race with the prior request's response landing.
    if (this.inflight) {
      try {
        await this.inflight;
      } catch {
        /* swallow; we're about to refetch */
      }
    }
    this.albums = [];
    this.nextOffset = 0;
    this.exhausted = false;
    await this.loadMore();
  }

  async loadMore(): Promise<void> {
    if (this.loading || this.exhausted) return;
    this.loading = true;
    this.inflight = (async () => {
      try {
        const res = await this.client.GET("/api/v1/albums", {
          params: { query: { limit: 100, offset: this.nextOffset ?? 0 } } as never,
        });
        if (res.error || !res.data) return;
        const data = res.data as { items?: AlbumListItem[]; next_offset?: number };
        const items = data.items ?? [];
        this.albums = [...this.albums, ...items];
        const next = data.next_offset ?? null;
        this.nextOffset = next;
        if (next === null) this.exhausted = true;
      } finally {
        this.loading = false;
        this.inflight = null;
      }
    })();
    await this.inflight;
  }

  async create(name: string): Promise<void> {
    const trimmed = name.trim();
    if (trimmed.length === 0) throw new Error("Name is required");
    if (trimmed.length > 200) throw new Error("Name exceeds 200 characters");
    const res = await this.client.POST("/api/v1/albums", { body: { name: trimmed } as never });
    if (res.error) throw res.error;
    // Refetch page 1 so backend ordering / cover derivation is honored.
    await this.loadInitial();
  }

  async rename(id: string, name: string): Promise<void> {
    const trimmed = name.trim();
    if (trimmed.length === 0) throw new Error("Name is required");
    if (trimmed.length > 200) throw new Error("Name exceeds 200 characters");
    const res = await this.client.PATCH("/api/v1/albums/{id}", {
      params: { path: { id } } as never,
      body: { name: trimmed } as never,
    });
    if (res.error) throw res.error;
    const updated = res.data as AlbumListItem;
    const idx = this.albums.findIndex((a) => a.id === id);
    if (idx >= 0) {
      const existing = this.albums[idx];
      if (existing) {
        this.albums = [
          ...this.albums.slice(0, idx),
          { ...existing, ...updated },
          ...this.albums.slice(idx + 1),
        ];
      }
    }
  }

  async delete(id: string): Promise<void> {
    const wasLoaded = this.albums.some((a) => a.id === id);
    const res = await this.client.DELETE("/api/v1/albums/{id}", {
      params: { path: { id } } as never,
    });
    if (res.error) throw res.error;
    if (wasLoaded) {
      this.albums = this.albums.filter((a) => a.id !== id);
      if (this.nextOffset !== null && this.nextOffset > 0) {
        this.nextOffset -= 1;
      }
    }
  }

  byId(id: string): AlbumListItem | undefined {
    return this.albums.find((a) => a.id === id);
  }
}

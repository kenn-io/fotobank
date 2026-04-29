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
  // True when the most recent load attempt failed. Routes use this to
  // gate auto-retry effects and to render an error state so a transient
  // 5xx doesn't cause the route to spin in an infinite refetch loop.
  loadError = $state(false);
  private nextOffset: number | null = 0;
  private inflight: Promise<void> | null = null;
  // Monotonic token bumped on every loadInitial(). Concurrent refreshes
  // capture the token and check it before mutating state, so a slower
  // earlier refresh can't overwrite a newer one's result.
  private initialToken = 0;

  constructor(private client: Pick<Client, "GET" | "POST" | "PATCH" | "DELETE">) {}

  async loadInitial(): Promise<void> {
    const token = ++this.initialToken;
    // Wait for any in-flight loadMore to settle so resetting state and
    // re-fetching doesn't race with the prior request's response landing.
    if (this.inflight) {
      try {
        await this.inflight;
      } catch {
        /* swallow; we're about to refetch */
      }
    }
    if (token !== this.initialToken) return;
    this.albums = [];
    this.nextOffset = 0;
    this.exhausted = false;
    this.loadError = false;
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
        if (res.error || !res.data) {
          this.loadError = true;
          // Mark exhausted so the auto-retry effects in routes don't
          // loop on a persistent failure. Manual retry should call
          // retry() to clear the flag and try again.
          this.exhausted = true;
          return;
        }
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

  // Manual retry after a load failure. Clears the error flag and the
  // exhausted-on-error gate, then reissues loadInitial.
  async retry(): Promise<void> {
    this.loadError = false;
    this.exhausted = false;
    await this.loadInitial();
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

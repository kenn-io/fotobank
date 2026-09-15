import type { Client } from "../api/client";

export type AlbumListItem = {
  id: string;
  name: string;
  created_at: string;
  updated_at: string;
  item_count: number;
  hidden_count?: number;
  cover?: { media_id: string; thumb_version: number };
};

// sortByUpdatedAtDesc mirrors the backend album ordering
// (`ORDER BY updated_at DESC, id ASC`). Compares parsed instants
// rather than raw RFC3339 strings — fractional seconds and varying
// timezone offsets break lexicographic comparison (e.g.
// "...00.1Z" sorts before "...00Z" lexically even though it is
// strictly later). Date.parse falls back to NaN for invalid input;
// we treat that as the epoch so a malformed value sorts to the
// bottom rather than poisoning the entire comparator.
function sortByUpdatedAtDesc(items: AlbumListItem[]): AlbumListItem[] {
  const ts = (s: string): number => {
    const t = Date.parse(s);
    return Number.isNaN(t) ? 0 : t;
  };
  return [...items].sort((a, b) => {
    const ta = ts(a.updated_at);
    const tb = ts(b.updated_at);
    if (ta !== tb) return tb - ta;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}

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
  // Stale flag: set by markStale() after a hide/unhide operation. Cleared
  // by refreshIfStale() after the refetch completes. AlbumsIndex calls
  // refreshIfStale() at mount, so the refetch is deferred until the user
  // navigates to the albums view rather than firing eagerly from any route.
  private stale = false;

  constructor(private client: Pick<Client, "createAlbum" | "deleteAlbum" | "listAlbums" | "renameAlbum">) {}

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
        const res = await this.client.listAlbums({ limit: 100, offset: this.nextOffset ?? 0 });
        if (res.error || !res.data) {
          // loadError gates auto-retry effects in routes (so a transient
          // 5xx doesn't spin in an infinite refetch loop), but we MUST
          // NOT set exhausted here — there are still pages on the server
          // we couldn't reach, and conflating the two would hide the
          // remaining "Load more" affordance and treat retry-able state
          // as terminal. Routes that auto-load must gate on
          // `!exhausted && !loadError`.
          this.loadError = true;
          return;
        }
        const data = res.data as unknown as { items?: AlbumListItem[]; next_offset?: number };
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

  // markStale flags the cached list as out-of-date. Call after a hide or
  // unhide operation so the album grid refreshes on next mount rather than
  // immediately. refreshIfStale() is the paired consumer.
  markStale(): void {
    this.stale = true;
  }

  // refreshIfStale refetches /api/v1/albums when the stale flag is set.
  // No-op otherwise. The stale flag is cleared only after a successful
  // reload so a failed request leaves it set for the next attempt (finding #15).
  async refreshIfStale(): Promise<void> {
    if (!this.stale) return;
    await this.loadInitial();
    // Clear stale only on success (no loadError). On error, preserve the
    // stale flag so the next mount retries instead of silently staying stale.
    if (!this.loadError) {
      this.stale = false;
    }
  }

  async create(name: string): Promise<string> {
    const trimmed = name.trim();
    if (trimmed.length === 0) throw new Error("Name is required");
    if (trimmed.length > 200) throw new Error("Name exceeds 200 characters");
    const res = await this.client.createAlbum({ name: trimmed });
    if (res.error) throw res.error;
    // Capture the new id BEFORE the refetch — the response body is the
    // created album. Returning the id lets callers (e.g. AddToAlbumModal)
    // select by id rather than name match, which would target the wrong
    // album when names duplicate.
    const created = res.data as { id: string };
    // Refetch page 1 so backend ordering / cover derivation is honored.
    await this.loadInitial();
    return created.id;
  }

  async rename(id: string, name: string): Promise<void> {
    const trimmed = name.trim();
    if (trimmed.length === 0) throw new Error("Name is required");
    if (trimmed.length > 200) throw new Error("Name exceeds 200 characters");
    const res = await this.client.renameAlbum(id, { name: trimmed });
    if (res.error) throw res.error;
    const updated = res.data as AlbumListItem;
    const idx = this.albums.findIndex((a) => a.id === id);
    if (idx >= 0) {
      const existing = this.albums[idx];
      if (existing) {
        const next = [...this.albums];
        next[idx] = { ...existing, ...updated };
        this.albums = sortByUpdatedAtDesc(next);
      }
    }
  }

  async delete(id: string): Promise<void> {
    const res = await this.client.deleteAlbum(id);
    if (res.error) throw res.error;
    this.dropLocal(id);
  }

  // Local-only mutators used by routes that already issued the network
  // call themselves (e.g. AlbumDetail rename/delete via AlbumDetailStore)
  // and just need to keep this list cache in sync. They don't refetch.
  applyRename(id: string, updates: { name: string; updated_at: string }): void {
    const idx = this.albums.findIndex((a) => a.id === id);
    if (idx < 0) return;
    const existing = this.albums[idx];
    if (!existing) return;
    // The backend orders albums by (updated_at DESC, id ASC); a rename
    // bumps updated_at so the renamed row should pop to the top of the
    // cached list. Without this re-sort the SPA would render a stale
    // position until the next refetch.
    const next = [...this.albums];
    next[idx] = { ...existing, name: updates.name, updated_at: updates.updated_at };
    this.albums = sortByUpdatedAtDesc(next);
  }

  dropLocal(id: string): void {
    const wasLoaded = this.albums.some((a) => a.id === id);
    if (!wasLoaded) return;
    this.albums = this.albums.filter((a) => a.id !== id);
    if (this.nextOffset !== null && this.nextOffset > 0) {
      this.nextOffset -= 1;
    }
  }

  byId(id: string): AlbumListItem | undefined {
    return this.albums.find((a) => a.id === id);
  }
}

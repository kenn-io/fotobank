import type { Client } from "../api/client";

export type ShareTargetType = "media_set" | "album_live";
export type ShareBrokerStatus =
  | "pending" | "active" | "failed" | "revoking" | "revoked_remote";

export type ScopeListRow = {
  uuid: string;
  target_type: ShareTargetType;
  target_album_id: string | null;
  target_summary: { label: string; item_count?: number } | null;
  grantee: { hub: string; user_id: string };
  grantee_handle?: string;
  allow_download: boolean;
  label: string;
  created_at: string;
  expires_at: string | null;
  revoked_at: string | null;
  broker_status: ShareBrokerStatus;
  broker_attempts: number;
  broker_last_error: string;
};

export type ScopeDetail = ScopeListRow & { media_ids?: string[] };
export type SharePreview = unknown; // shape comes from /preview endpoint; consumer renders raw

const POLL_INTERVAL_MS = 5000;

export type CreateShareInput =
  | {
      target_type: "media_set";
      media_ids: string[];
      grantee: { hub: string; user_id: string };
      label: string;
      allow_download: boolean;
    }
  | {
      target_type: "album_live";
      album_id: string;
      grantee: { hub: string; user_id: string };
      label: string;
      allow_download: boolean;
    };

export class SharesStore {
  scopes = $state<ScopeListRow[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  showRevoked = $state(false);
  albumIDFilter = $state<string | null>(null);

  private nextOffset: number | null = 0;
  private pollHandle: ReturnType<typeof setInterval> | null = null;
  private detailCache = new Map<string, ScopeDetail>();
  private previewCache = new Map<string, SharePreview>();

  constructor(private client: Pick<Client, "GET" | "POST" | "DELETE">) {}

  async loadInitial(): Promise<void> {
    this.scopes = [];
    this.nextOffset = 0;
    this.exhausted = false;
    await this.loadMore();
    this.maybeStartPolling();
  }

  async loadMore(): Promise<void> {
    if (this.loading || this.exhausted) return;
    this.loading = true;
    try {
      const query: Record<string, unknown> = {
        limit: 100,
        offset: this.nextOffset ?? 0,
        include_settled: this.showRevoked,
      };
      if (this.albumIDFilter) query["album_id"] = this.albumIDFilter;
      const res = await this.client.GET("/api/v1/shares", { params: { query } as never });
      if (res.error || !res.data) return;
      const data = res.data as { items?: ScopeListRow[]; next_offset?: number | null };
      const items = data.items ?? [];
      this.scopes = [...this.scopes, ...items];
      const next = data.next_offset ?? null;
      this.nextOffset = next;
      if (next === null) this.exhausted = true;
    } finally {
      this.loading = false;
    }
  }

  async create(input: CreateShareInput): Promise<void> {
    const res = await this.client.POST("/api/v1/shares", { body: input as never });
    if (res.error) throw res.error;
    await this.refetchListPreservingFilter();
    this.maybeStartPolling();
  }

  async revoke(uuid: string): Promise<void> {
    const res = await this.client.POST("/api/v1/shares/{uuid}/revoke", {
      params: { path: { uuid } } as never,
    });
    if (res.error) throw res.error;
    this.detailCache.delete(uuid);
    this.previewCache.delete(uuid);
    await this.refetchListPreservingFilter();
    this.maybeStartPolling();
  }

  async retry(uuid: string): Promise<void> {
    const res = await this.client.POST("/api/v1/shares/{uuid}/retry", {
      params: { path: { uuid } } as never,
    });
    if (res.error) throw res.error;
    this.detailCache.delete(uuid);
    await this.refetchListPreservingFilter();
    this.maybeStartPolling();
  }

  async getDetail(uuid: string): Promise<ScopeDetail | null> {
    const cached = this.detailCache.get(uuid);
    if (cached) return cached;
    const res = await this.client.GET("/api/v1/shares/{uuid}", {
      params: { path: { uuid } } as never,
    });
    if (res.error || !res.data) return null;
    const det = res.data as ScopeDetail;
    this.detailCache.set(uuid, det);
    return det;
  }

  async getPreview(uuid: string): Promise<SharePreview | null> {
    const cached = this.previewCache.get(uuid);
    if (cached) return cached;
    const res = await this.client.GET("/api/v1/shares/{uuid}/preview", {
      params: { path: { uuid } } as never,
    });
    if (res.error || !res.data) return null;
    this.previewCache.set(uuid, res.data);
    return res.data;
  }

  setShowRevoked(v: boolean): void {
    if (this.showRevoked === v) return;
    this.showRevoked = v;
    this.refetchListPreservingFilter();
  }

  setAlbumIDFilter(v: string | null): void {
    if (this.albumIDFilter === v) return;
    this.albumIDFilter = v;
    this.refetchListPreservingFilter();
  }

  private async refetchListPreservingFilter(): Promise<void> {
    this.scopes = [];
    this.nextOffset = 0;
    this.exhausted = false;
    await this.loadMore();
  }

  private maybeStartPolling(): void {
    const needs = this.scopes.some(
      (s) => s.broker_status === "pending" || s.broker_status === "revoking",
    );
    if (needs && this.pollHandle === null) {
      this.pollHandle = setInterval(() => this.poll(), POLL_INTERVAL_MS);
    } else if (!needs && this.pollHandle !== null) {
      clearInterval(this.pollHandle);
      this.pollHandle = null;
    }
  }

  private async poll(): Promise<void> {
    // Polling MUST NOT replace the list — that would drop rows the
    // user has already paginated past. Refetch the first 200 rows
    // (covers any sane pending-row count) and merge by uuid into the
    // current scopes. Older rows update on the next user-driven
    // loadMore (acceptable: settled rows don't change state, and
    // pending rows are almost always recent).
    //
    // include_settled is true regardless of the user's filter: a row
    // transitioning revoking → revoked must be observable so polling
    // can stop. If we honored showRevoked here, a revoked row would
    // drop out of the response and the local copy would stay stuck at
    // "revoking" forever, polling indefinitely. The user-facing filter
    // is applied in the route view, not at the polling boundary.
    const query: Record<string, unknown> = {
      limit: 200,
      offset: 0,
      include_settled: true,
    };
    if (this.albumIDFilter) query["album_id"] = this.albumIDFilter;
    const res = await this.client.GET("/api/v1/shares", { params: { query } as never });
    if (res.error || !res.data) return;
    const data = res.data as { items?: ScopeListRow[] };
    const fresh = new Map<string, ScopeListRow>();
    for (const row of data.items ?? []) fresh.set(row.uuid, row);
    this.scopes = this.scopes.map((row) => fresh.get(row.uuid) ?? row);
    this.maybeStartPolling();
  }

  stopPolling(): void {
    if (this.pollHandle !== null) {
      clearInterval(this.pollHandle);
      this.pollHandle = null;
    }
  }
}

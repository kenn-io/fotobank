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
  // True when the most recent load attempt failed. Routes use this to
  // gate auto-retry effects and to render an error state so a transient
  // 5xx doesn't cause the route to spin in an infinite refetch loop.
  loadError = $state(false);
  showRevoked = $state(false);
  albumIDFilter = $state<string | null>(null);

  private nextOffset: number | null = 0;
  // Recursive setTimeout (not setInterval) so the next tick is only
  // scheduled after the current poll's fetch resolves — overlapping
  // polls could land out of order and merge stale rows over fresh.
  private pollHandle: ReturnType<typeof setTimeout> | null = null;
  private detailCache = new Map<string, ScopeDetail>();
  // previewCache is intentionally not cleared by retry(): preview content
  // is broker-cached and bound to the share's identity, not its broker_status,
  // so a retry that re-issues the broker grant doesn't change what /preview
  // returns. detailCache is cleared because broker_status is part of the
  // detail DTO and retry transitions it.
  private previewCache = new Map<string, SharePreview>();
  // Monotonic token bumped on every state-clearing call (loadInitial,
  // refetchListPreservingFilter). Async fetches capture the token at
  // entry and check it before mutating state, so a slower stale response
  // can't overwrite a newer one's result.
  private loadToken = 0;

  constructor(private client: Pick<Client, "sharesCreate" | "sharesGet" | "sharesList" | "sharesPreview" | "sharesRetry" | "sharesRevoke">) {}

  async loadInitial(): Promise<void> {
    const token = ++this.loadToken;
    this.scopes = [];
    this.nextOffset = 0;
    this.exhausted = false;
    this.loadError = false;
    // Reset loading so a new loadMore can fetch even if a stale one is
    // still resolving — its response will be dropped by the token check.
    this.loading = false;
    await this.loadMore(token);
    if (token !== this.loadToken) return;
    this.maybeStartPolling();
  }

  async loadMore(token?: number): Promise<void> {
    const t = token ?? this.loadToken;
    if (this.loading || this.exhausted) return;
    this.loading = true;
    try {
      const query: Record<string, unknown> = {
        limit: 100,
        offset: this.nextOffset ?? 0,
        include_settled: this.showRevoked,
      };
      if (this.albumIDFilter) query["album_id"] = this.albumIDFilter;
      const res = await this.client.sharesList(query);
      if (t !== this.loadToken) return;
      if (res.error || !res.data) {
        this.loadError = true;
        // Mark exhausted so any auto-retry effect in the route doesn't
        // loop on a persistent failure. Manual retry via retryLoad()
        // clears the flag.
        this.exhausted = true;
        return;
      }
      const data = res.data as unknown as { items?: ScopeListRow[]; next_offset?: number | null };
      const items = data.items ?? [];
      this.scopes = [...this.scopes, ...items];
      const next = data.next_offset ?? null;
      this.nextOffset = next;
      if (next === null) this.exhausted = true;
    } finally {
      // Only clear loading if we're still the active token; otherwise a
      // newer loadInitial / refetch owns the flag and we shouldn't reset it.
      if (t === this.loadToken) this.loading = false;
    }
  }

  // Manual retry after a load failure. Clears the error flag and the
  // exhausted-on-error gate, then reissues loadInitial.
  async retryLoad(): Promise<void> {
    this.loadError = false;
    this.exhausted = false;
    await this.loadInitial();
  }

  async create(input: CreateShareInput): Promise<void> {
    const res = await this.client.sharesCreate(input);
    if (res.error) throw res.error;
    await this.refetchListPreservingFilter();
    this.maybeStartPolling();
  }

  async revoke(uuid: string): Promise<void> {
    const res = await this.client.sharesRevoke(uuid);
    if (res.error) throw res.error;
    this.detailCache.delete(uuid);
    this.previewCache.delete(uuid);
    await this.refetchListPreservingFilter();
    this.maybeStartPolling();
  }

  async retry(uuid: string): Promise<void> {
    const res = await this.client.sharesRetry(uuid);
    if (res.error) throw res.error;
    this.detailCache.delete(uuid);
    await this.refetchListPreservingFilter();
    this.maybeStartPolling();
  }

  async getDetail(uuid: string): Promise<ScopeDetail | null> {
    const cached = this.detailCache.get(uuid);
    if (cached) return cached;
    const res = await this.client.sharesGet(uuid);
    if (res.error || !res.data) return null;
    const det = res.data as ScopeDetail;
    this.detailCache.set(uuid, det);
    return det;
  }

  async getPreview(uuid: string): Promise<SharePreview | null> {
    const cached = this.previewCache.get(uuid);
    if (cached) return cached;
    const res = await this.client.sharesPreview(uuid);
    if (res.error || !res.data) return null;
    this.previewCache.set(uuid, res.data);
    return res.data;
  }

  async setShowRevoked(v: boolean): Promise<void> {
    if (this.showRevoked === v) return;
    this.showRevoked = v;
    await this.refetchListPreservingFilter();
  }

  async setAlbumIDFilter(v: string | null): Promise<void> {
    if (this.albumIDFilter === v) return;
    this.albumIDFilter = v;
    await this.refetchListPreservingFilter();
  }

  private async refetchListPreservingFilter(): Promise<void> {
    const token = ++this.loadToken;
    this.scopes = [];
    this.nextOffset = 0;
    this.exhausted = false;
    this.loadError = false;
    // Reset loading so a new loadMore can fetch even if a stale one is
    // still resolving — its response will be dropped by the token check.
    this.loading = false;
    await this.loadMore(token);
    // The refetch may have brought new pending/revoking rows into view
    // (e.g. setShowRevoked(true) surfaces revoking shares that were
    // previously filtered out). Restart polling if the new list needs
    // it, or stop it if every visible row is now settled. Token check:
    // skip if a newer state-clearing call has already run.
    if (token !== this.loadToken) return;
    this.maybeStartPolling();
  }

  private maybeStartPolling(): void {
    const needs = this.scopes.some(
      (s) => s.broker_status === "pending" || s.broker_status === "revoking",
    );
    if (needs && this.pollHandle === null) {
      this.schedulePoll();
    } else if (!needs && this.pollHandle !== null) {
      clearTimeout(this.pollHandle);
      this.pollHandle = null;
    }
  }

  private schedulePoll(): void {
    // Recursive setTimeout: poll() runs maybeStartPolling() in a
    // finally so the next tick is scheduled after every poll attempt
    // (success OR error) so long as the token is still current. This
    // serializes polls — a slow fetch can't overlap with a new tick
    // and produce out-of-order merges. The trade-off is a slight cadence
    // skew (POLL_INTERVAL_MS + fetch latency between calls) which is
    // acceptable for a 5s polling window.
    this.pollHandle = setTimeout(() => {
      // pollHandle is cleared here so maybeStartPolling() at the end of
      // poll() sees a null handle and re-schedules. Without this, the
      // "needs && this.pollHandle === null" branch would never fire.
      this.pollHandle = null;
      void this.poll();
    }, POLL_INTERVAL_MS);
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
    const token = this.loadToken;
    const query: Record<string, unknown> = {
      limit: 200,
      offset: 0,
      include_settled: true,
    };
    if (this.albumIDFilter) query["album_id"] = this.albumIDFilter;
    try {
      const res = await this.client.sharesList(query);
      // Drop the response if a user-initiated state-clearing call ran while
      // we were waiting; otherwise we'd merge stale data into the fresh list.
      if (token !== this.loadToken) return;
      if (res.error || !res.data) return;
      const data = res.data as unknown as { items?: ScopeListRow[] };
      const fresh = new Map<string, ScopeListRow>();
      for (const row of data.items ?? []) fresh.set(row.uuid, row);
      this.scopes = this.scopes.map((row) => fresh.get(row.uuid) ?? row);
    } finally {
      // Re-arm on every path (success, transient error, or thrown
      // rejection) when the token is still current — without this, a
      // single failed GET would silently kill polling because the
      // setTimeout already fired and pollHandle was cleared at fire
      // time. The token guard prevents a stale poll from re-arming
      // when a fresh user-driven action (loadInitial / refetch) is in
      // flight — that path will arm its own poll after the new fetch
      // resolves.
      if (token === this.loadToken) this.maybeStartPolling();
    }
  }

  stopPolling(): void {
    if (this.pollHandle !== null) {
      clearTimeout(this.pollHandle);
      this.pollHandle = null;
    }
  }
}

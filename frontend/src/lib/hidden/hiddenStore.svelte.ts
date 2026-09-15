// frontend/src/lib/hidden/hiddenStore.svelte.ts

import type { Client } from "../api/client";

export type HiddenError =
  | { kind: "wrong_passcode" }
  | { kind: "locked_out"; retryAfterSeconds: number }
  | { kind: "invalid_input" }
  | { kind: "identity_required" }
  | { kind: "network"; message: string };

export type HiddenBulkResult = {
  succeeded: string[] | null;
  failed: Array<{ id: string; code: string }> | null;
};

export class HiddenStore {
  configured = $state(false);
  unlocked = $state(false);
  expiresAt = $state<string | null>(null);
  lockedOutUntil = $state<string | null>(null);
  error = $state<HiddenError | null>(null);

  constructor(private client: Pick<Client, "hiddenLock" | "hiddenState" | "hiddenUnlock" | "hideMediaBulk" | "unhideMediaBulk">) {}

  async refresh(): Promise<void> {
    const res = await this.client.hiddenState() as {
      data?: { configured: boolean; unlocked: boolean; expires_at?: string };
      error?: unknown;
    };
    if (res.error || !res.data) return;
    const data = res.data;
    this.configured = data.configured;
    this.unlocked = data.unlocked;
    this.expiresAt = data.expires_at ?? null;
  }

  async unlock(passcode: string): Promise<void> {
    const res = await this.client.hiddenUnlock({ passcode });
    if (res.error) {
      const err = res.error as { status?: number; headers?: Record<string, string> };
      const status = err.status ?? 0;
      if (status === 403) {
        this.error = { kind: "wrong_passcode" };
      } else if (status === 429) {
        const retryHeader = err.headers?.["retry-after"] ?? "300";
        const retryAfterSeconds = parseInt(retryHeader, 10) || 300;
        this.error = { kind: "locked_out", retryAfterSeconds };
      } else if (status === 400) {
        this.error = { kind: "invalid_input" };
      } else if (status === 401) {
        this.error = { kind: "identity_required" };
      } else {
        this.error = { kind: "network", message: "request failed" };
      }
      throw this.error;
    }
    this.error = null;
    await this.refresh();
  }

  async lock(opts?: { keepalive?: boolean }): Promise<void> {
    // Optimistic clear so UI updates even if network call is cancelled
    // by tab teardown.
    this.unlocked = false;
    this.expiresAt = null;
    if (opts?.keepalive) {
      void this.client.hiddenLock({ keepalive: true });
      return;
    }
    await this.client.hiddenLock();
    await this.refresh();
  }

  async hide(ids: string[]): Promise<HiddenBulkResult> {
    const res = await this.client.hideMediaBulk({ media_ids: ids });
    if (res.error) throw res.error;
    return res.data as HiddenBulkResult;
  }

  async unhide(ids: string[]): Promise<HiddenBulkResult> {
    const res = await this.client.unhideMediaBulk({ media_ids: ids });
    if (res.error) {
      const err = res.error as { status?: number };
      if (err.status === 403) {
        // Cookie expired mid-action; refresh state so the gate re-appears.
        await this.refresh();
      }
      throw res.error;
    }
    return res.data as HiddenBulkResult;
  }
}

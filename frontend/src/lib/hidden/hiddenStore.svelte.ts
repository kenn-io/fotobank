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

  constructor(private client: Pick<Client, "GET" | "POST">) {}

  async refresh(): Promise<void> {
    const res = await this.client.GET("/api/v1/auth/hidden/state", {} as never) as {
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
    const res = await this.client.POST("/api/v1/auth/hidden/unlock", {
      body: { passcode } as never,
    });
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
      void fetch("/api/v1/auth/hidden/lock", { method: "POST", keepalive: true });
      return;
    }
    await this.client.POST("/api/v1/auth/hidden/lock", {} as never);
    await this.refresh();
  }

  async hide(ids: string[]): Promise<HiddenBulkResult> {
    const res = await this.client.POST("/api/v1/media/hidden:bulk", {
      body: { media_ids: ids } as never,
    });
    if (res.error) throw res.error;
    return res.data as HiddenBulkResult;
  }

  async unhide(ids: string[]): Promise<HiddenBulkResult> {
    const res = await this.client.POST("/api/v1/media/unhide:bulk", {
      body: { media_ids: ids } as never,
    });
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

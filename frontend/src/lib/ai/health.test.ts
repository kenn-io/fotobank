import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AIHealthStore, deriveDot } from "./health.svelte";
import type { AIHealth } from "./types";

const base: AIHealth = {
  enabled: true,
  paused_reason: "",
  vision: { reachable: true, last_check_at: "2026-04-30T18:42:11Z" },
  tag: {
    active_fingerprint: "fp",
    pending: 0,
    working: 0,
    blocked: 0,
    failed_active: 0,
    skipped: 0,
    done: 0,
    throughput_per_min: 0,
  },
  caption: {
    active_fingerprint: "fp",
    pending: 0,
    working: 0,
    blocked: 0,
    failed_active: 0,
    skipped: 0,
    done: 0,
    throughput_per_min: 0,
  },
};

describe("deriveDot", () => {
  it("hides when null", () => {
    expect(deriveDot(null).state).toBe("hidden");
  });

  it("hides when config_disabled", () => {
    expect(deriveDot({ ...base, paused_reason: "config_disabled" }).state).toBe("hidden");
  });

  it("paused on ack required", () => {
    expect(deriveDot({ ...base, paused_reason: "acknowledgement_required" }).state).toBe("paused");
  });

  it("unreachable when vision not reachable", () => {
    expect(
      deriveDot({ ...base, vision: { ...base.vision, reachable: false } }).state,
    ).toBe("unreachable");
  });

  it("failing when threshold exceeded", () => {
    expect(deriveDot({ ...base, tag: { ...base.tag, failed_active: 11 } }).state).toBe("failing");
  });

  it("backlog when pending exceeds threshold", () => {
    expect(deriveDot({ ...base, tag: { ...base.tag, pending: 1001 } }).state).toBe("backlog");
  });

  it("idle otherwise", () => {
    expect(deriveDot(base).state).toBe("idle");
  });

  it("prefers paused over unreachable when both apply", () => {
    expect(
      deriveDot({
        ...base,
        paused_reason: "acknowledgement_required",
        vision: { ...base.vision, reachable: false },
      }).state,
    ).toBe("paused");
  });

  it("counts both tag and caption toward failing threshold", () => {
    expect(
      deriveDot({
        ...base,
        tag: { ...base.tag, failed_active: 6 },
        caption: { ...base.caption, failed_active: 6 },
      }).state,
    ).toBe("failing");
  });
});

describe("AIHealthStore.refresh", () => {
  const mockFetch = vi.fn();

  beforeEach(() => {
    vi.stubGlobal("fetch", mockFetch);
    mockFetch.mockReset();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("collapses a burst into at most one inflight + one chained fetch", async () => {
    // Refresh semantics: a single call fires one fetch. If additional
    // refresh() calls land while one is in flight, they all collapse
    // onto the same promise AND mark the snapshot stale, so exactly
    // one more fetch fires after the inflight settles. This catches
    // an `ai.health.changed` event that lands during the initial
    // mount fetch — without the chained fetch the store would keep
    // the pre-event snapshot until the next event.
    type Resolver = (value: AIHealth) => void;
    const first: { current: Resolver | null } = { current: null };
    const firstBody = new Promise<AIHealth>((res) => {
      first.current = res;
    });
    mockFetch.mockReturnValueOnce({ ok: true, json: () => firstBody });

    const store = new AIHealthStore();
    const a = store.refresh();
    // Three more concurrent refreshes — they all collapse onto one
    // chained follow-up, not three.
    const b = store.refresh();
    const c = store.refresh();
    const d = store.refresh();
    expect(mockFetch).toHaveBeenCalledTimes(1);

    // Wire up the chained fetch's response BEFORE letting the first
    // settle so the IIFE's do/while finds the mock ready.
    const next: AIHealth = { ...base, tag: { ...base.tag, pending: 9 } };
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => next });

    const resolve = first.current;
    if (!resolve) throw new Error("expected refresh to wire up the body resolver");
    resolve(base);
    await Promise.all([a, b, c, d]);

    // Final state reflects the chained fetch (the freshest snapshot).
    expect(store.health).toEqual(next);
    // Exactly two network calls regardless of burst size.
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("a single refresh fires exactly one fetch (no follow-up)", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => base });
    const store = new AIHealthStore();
    await store.refresh();
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(store.health).toEqual(base);
  });

  it("allows a fresh fetch after the inflight promise settles", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => base });
    const store = new AIHealthStore();
    await store.refresh();
    expect(store.health).toEqual(base);

    const next: AIHealth = { ...base, tag: { ...base.tag, pending: 7 } };
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => next });
    await store.refresh();
    expect(store.health).toEqual(next);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("clears inflight after a failed fetch so the next refresh can retry", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 500 });
    const store = new AIHealthStore();
    await expect(store.refresh()).rejects.toThrow(/500/);

    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => base });
    await store.refresh();
    expect(store.health).toEqual(base);
    expect(mockFetch).toHaveBeenCalledTimes(2);
  });
});

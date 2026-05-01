import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { backfillAI, getAIHealth, retryPhotoAI } from "./client";

const mockFetch = vi.fn();

beforeEach(() => {
  vi.stubGlobal("fetch", mockFetch);
  mockFetch.mockReset();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("getAIHealth", () => {
  it("returns parsed JSON", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        enabled: true,
        paused_reason: "",
        vision: { reachable: true, last_check_at: "2026-04-30T18:42:11Z" },
        tag: {
          active_fingerprint: "m|tags-v1|ip",
          pending: 0,
          working: 0,
          blocked: 0,
          failed_active: 0,
          skipped: 0,
          done: 0,
          throughput_per_min: 0,
        },
        caption: {
          active_fingerprint: "m|caption-v1|ip",
          pending: 0,
          working: 0,
          blocked: 0,
          failed_active: 0,
          skipped: 0,
          done: 0,
          throughput_per_min: 0,
        },
      }),
    });
    const h = await getAIHealth();
    expect(h.tag.active_fingerprint).toBe("m|tags-v1|ip");
  });
});

describe("backfillAI", () => {
  it("posts force=true correctly", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ enqueued: 5 }) });
    const out = await backfillAI("tag", { force: true });
    expect(out.enqueued).toBe(5);
    const args = mockFetch.mock.calls[0];
    expect(args).toBeDefined();
    if (!args) throw new Error("expected fetch to have been called");
    expect(args[0]).toBe("/api/v1/ai/backfill");
    const init = args[1] as RequestInit | undefined;
    if (!init) throw new Error("expected fetch init arg");
    expect(JSON.parse(init.body as string)).toEqual({
      task: "tag",
      force: true,
      scope: "all",
    });
  });
});

describe("retryPhotoAI", () => {
  it("404s surface as errors", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 404 });
    await expect(retryPhotoAI("m1", "tag")).rejects.toThrow(/404/);
  });
});

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { backfillAI, getAIHealth, getMediaAIView, retryPhotoAI } from "./client";

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
      text: async () => JSON.stringify(({
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
      })),
    });
    const h = await getAIHealth();
    expect(h.tag.active_fingerprint).toBe("m|tags-v1|ip");
  });
});

describe("backfillAI", () => {
  it("posts force=true correctly", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, text: async () => JSON.stringify(({ enqueued: 5 })) });
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
    mockFetch.mockResolvedValueOnce({ ok: false, status: 404 , text: async () => "" });
    await expect(retryPhotoAI("m1", "tag")).rejects.toThrow(/404/);
  });
});

describe("getMediaAIView", () => {
  it("returns parsed AIMediaView for a media", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      text: async () => JSON.stringify(({
        tags: [{ key: "dog", label: "Dog", rank: 1 }],
        caption: {
          text: "A small dog.",
          model_id: "qwen2.5-vl:3b",
          prompt_version: "caption-v1",
          generated_at: "2026-04-30T18:42:11Z",
        },
      })),
    });
    const out = await getMediaAIView("m1");
    expect(out.tags).toEqual([{ key: "dog", label: "Dog", rank: 1 }]);
    expect(out.caption?.text).toBe("A small dog.");
    const args = mockFetch.mock.calls[0];
    if (!args) throw new Error("expected fetch to have been called");
    expect(args[0]).toBe("/api/v1/media/m1/ai");
  });

  it("URL-encodes the media id", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, text: async () => JSON.stringify(({})) });
    await getMediaAIView("m/1+2");
    const args = mockFetch.mock.calls[0];
    if (!args) throw new Error("expected fetch to have been called");
    expect(args[0]).toBe("/api/v1/media/m%2F1%2B2/ai");
  });

  it("non-200 surfaces as an error", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 500 , text: async () => "" });
    await expect(getMediaAIView("m1")).rejects.toThrow(/500/);
  });
});

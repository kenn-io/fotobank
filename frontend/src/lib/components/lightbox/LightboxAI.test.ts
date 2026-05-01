import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/svelte";
import LightboxAI from "./LightboxAI.svelte";
import * as client from "../../ai/client";

vi.mock("../../ai/client", () => ({
  getMediaAIView: vi.fn(),
  retryPhotoAI: vi.fn(),
}));

beforeEach(() => {
  vi.mocked(client.getMediaAIView).mockReset();
  vi.mocked(client.retryPhotoAI).mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("LightboxAI", () => {
  it("renders tag chips and caption with provenance", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({
      tags: [
        { key: "dog", label: "Dog", rank: 1 },
        { key: "beach", label: "Beach", rank: 2 },
      ],
      caption: {
        text: "A small dog on a beach.",
        model_id: "qwen2.5-vl:3b",
        prompt_version: "caption-v1",
        generated_at: new Date().toISOString(),
      },
    });
    render(LightboxAI, { props: { mediaId: "m1" } });
    await waitFor(() => expect(screen.getByText("Dog")).toBeTruthy());
    expect(screen.getByText("Beach")).toBeTruthy();
    expect(screen.getByText(/A small dog on a beach/)).toBeTruthy();
    const link = screen.getByText("qwen2.5-vl:3b").closest("a");
    expect(link?.getAttribute("href")).toBe("/settings/ai");
  });

  it("renders +N more when tag count exceeds the cap", async () => {
    const tags = Array.from({ length: 15 }, (_, i) => ({
      key: `t${i}`,
      label: `Tag${i}`,
      rank: i + 1,
    }));
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({ tags });
    render(LightboxAI, { props: { mediaId: "m1" } });
    await waitFor(() => expect(screen.getByText("Tag0")).toBeTruthy());
    expect(screen.getByText("+3 more")).toBeTruthy();
  });

  it("renders caption failure inline with retry button", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({
      caption_failure: { kind: "malformed", message: "bad json" },
    });
    render(LightboxAI, { props: { mediaId: "m1" } });
    await waitFor(() => expect(screen.getByText(/Caption failed/)).toBeTruthy());
    const btn = screen.getByText("Retry");
    expect(btn).toBeTruthy();
  });

  it("renders tag failure inline with retry button", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({
      tag_failure: { kind: "provider_4xx", message: "bad" },
    });
    render(LightboxAI, { props: { mediaId: "m1" } });
    await waitFor(() => expect(screen.getByText(/Tags failed/)).toBeTruthy());
  });

  it("hides entirely on video skip", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({
      skipped: { reason: "video" },
    });
    const { container } = render(LightboxAI, { props: { mediaId: "m1" } });
    await waitFor(() => expect(vi.mocked(client.getMediaAIView)).toHaveBeenCalled());
    // Allow microtasks to settle.
    await new Promise((r) => setTimeout(r, 10));
    expect(container.querySelector("section.ai")).toBeNull();
  });

  it("renders no_preview skip with explanatory copy", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({
      skipped: { reason: "no_preview" },
    });
    render(LightboxAI, { props: { mediaId: "m1" } });
    await waitFor(() =>
      expect(screen.getByText(/AI not available/)).toBeTruthy(),
    );
  });

  it("renders pending placeholders when neither tags nor caption are present", async () => {
    vi.mocked(client.getMediaAIView).mockResolvedValueOnce({});
    render(LightboxAI, { props: { mediaId: "m1" } });
    await waitFor(() => expect(screen.getByText("Tags pending")).toBeTruthy());
    expect(screen.getByText("Caption pending")).toBeTruthy();
  });

  it("falls back to AI unavailable on fetch error", async () => {
    vi.mocked(client.getMediaAIView).mockRejectedValueOnce(new Error("boom"));
    render(LightboxAI, { props: { mediaId: "m1" } });
    await waitFor(() =>
      expect(screen.getByText(/AI unavailable/)).toBeTruthy(),
    );
  });

  it("ignores stale responses when mediaId changes mid-fetch", async () => {
    // Regression for the lightbox race where rapid prop changes
    // (mediaId A → B) could let A's slow response overwrite B's view.
    // Strategy: deferred promises for both fetches, resolve A AFTER B
    // so the test would fail without the monotonic-token guard.
    type Resolver = (value: import("../../ai/client").AIMediaView) => void;
    const resolveA: { current: Resolver | null } = { current: null };
    const resolveB: { current: Resolver | null } = { current: null };

    const promiseA = new Promise<import("../../ai/client").AIMediaView>((res) => {
      resolveA.current = res;
    });
    const promiseB = new Promise<import("../../ai/client").AIMediaView>((res) => {
      resolveB.current = res;
    });
    vi.mocked(client.getMediaAIView)
      .mockReturnValueOnce(promiseA)
      .mockReturnValueOnce(promiseB);

    const { rerender } = render(LightboxAI, { props: { mediaId: "A" } });
    // Switch to B before A's fetch settles.
    await rerender({ mediaId: "B" });

    const a = resolveA.current;
    const b = resolveB.current;
    if (!a || !b) throw new Error("expected both resolvers to be wired");

    // Resolve B first with B's distinctive caption — that should commit.
    b({
      caption: {
        text: "B-distinctive-caption",
        model_id: "qwen2.5-vl:3b",
        prompt_version: "caption-v1",
        generated_at: new Date().toISOString(),
      },
    });
    await waitFor(() => expect(screen.getByText("B-distinctive-caption")).toBeTruthy());

    // Now resolve A with A's distinctive video skip — under the bug,
    // this would overwrite B's view and hide the section. The token
    // guard must drop A's stale result instead.
    a({ skipped: { reason: "video" } });
    // Give the suppressed update a chance to (incorrectly) commit.
    await new Promise((r) => setTimeout(r, 20));

    // B's caption is still visible; A's stale "video" skip did not
    // hide the section.
    expect(screen.getByText("B-distinctive-caption")).toBeTruthy();
  });
});

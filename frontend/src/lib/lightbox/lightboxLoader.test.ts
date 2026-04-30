import { describe, it, expect, vi, beforeEach } from "vitest";
import { LightboxLoader, thumbUrl } from "./lightboxLoader";

// Stub HTMLImageElement so we can control load resolution/timing.
class FakeImage {
  src = "";
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  decode(): Promise<void> {
    return Promise.resolve();
  }
}

beforeEach(() => {
  (globalThis as unknown as { Image: typeof Image }).Image =
    FakeImage as unknown as typeof Image;
});

describe("thumbUrl", () => {
  it("builds the size-qualified thumb URL", () => {
    expect(thumbUrl("abc", "preview", 7)).toBe("/api/v1/media/abc/thumb?size=preview&v=7");
    expect(thumbUrl("abc", "large", 0)).toBe("/api/v1/media/abc/thumb?size=large&v=0");
    expect(thumbUrl("abc", "grid", 3)).toBe("/api/v1/media/abc/thumb?size=grid&v=3");
  });
});

describe("LightboxLoader.load", () => {
  it("uses grid URL as initial source when provided", async () => {
    const loader = new LightboxLoader();
    const onSrc = vi.fn();
    await loader.load({
      activeId: "m1",
      gridSrc: "/grid",
      previewUrl: "/preview",
      largeUrl: "/large",
      onSrc,
    });
    expect(onSrc).toHaveBeenNthCalledWith(1, "/grid");
  });

  it("starts at preview when no gridSrc is provided", async () => {
    const loader = new LightboxLoader();
    const onSrc = vi.fn();
    await loader.load({
      activeId: "m1",
      gridSrc: null,
      previewUrl: "/preview",
      largeUrl: "/large",
      onSrc,
    });
    // First emission should be the preview URL once it loads.
    // (Test setup auto-resolves load; we only assert ordering.)
    const seq = onSrc.mock.calls.map((c) => c[0]);
    expect(seq[0]).toBe("/preview");
  });

  it("logical cancellation drops stale completions", async () => {
    const loader = new LightboxLoader();
    const onSrcA = vi.fn();
    const onSrcB = vi.fn();
    const a = loader.load({
      activeId: "a",
      gridSrc: null,
      previewUrl: "/a-preview",
      largeUrl: "/a-large",
      onSrc: onSrcA,
    });
    // Switch to B before A finishes.
    loader.cancelAll();
    const b = loader.load({
      activeId: "b",
      gridSrc: null,
      previewUrl: "/b-preview",
      largeUrl: "/b-large",
      onSrc: onSrcB,
    });
    await Promise.all([a, b]);
    // onSrcA must not have produced /a-large after cancelAll.
    expect(onSrcA.mock.calls.some((c) => c[0] === "/a-large")).toBe(false);
  });
});

describe("LightboxLoader.prefetch", () => {
  it("orders preview prev/next first, large second", async () => {
    const loader = new LightboxLoader();
    const order: string[] = [];
    const fakeFetch = vi.fn().mockImplementation((url: string) => {
      order.push(url);
      return Promise.resolve();
    });
    loader.setFetcher(fakeFetch);
    loader.prefetch({
      prevPreviewUrl: "/p-prev",
      nextPreviewUrl: "/p-next",
      prevLargeUrl: "/l-prev",
      nextLargeUrl: "/l-next",
    });
    // Drain microtasks so the in-flight cap (2) cycles through the queue.
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(order.slice(0, 2).sort()).toEqual(["/p-next", "/p-prev"]);
    expect(order.slice(2).sort()).toEqual(["/l-next", "/l-prev"]);
  });

  it("caps in-flight prefetches at 2", async () => {
    const loader = new LightboxLoader();
    let inflight = 0;
    let peak = 0;
    const fakeFetch = vi.fn().mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          inflight += 1;
          peak = Math.max(peak, inflight);
          setTimeout(() => {
            inflight -= 1;
            resolve();
          }, 10);
        }),
    );
    loader.setFetcher(fakeFetch);
    loader.prefetch({
      prevPreviewUrl: "/p-prev",
      nextPreviewUrl: "/p-next",
      prevLargeUrl: "/l-prev",
      nextLargeUrl: "/l-next",
    });
    await new Promise((r) => setTimeout(r, 50));
    expect(peak).toBeLessThanOrEqual(2);
  });
});

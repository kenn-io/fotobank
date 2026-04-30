import { describe, it, expect, vi, beforeEach } from "vitest";
import { ScrollRestore } from "./scrollRestore.svelte";

function setBody(scrollHeight: number) {
  Object.defineProperty(document.body, "scrollHeight", {
    configurable: true,
    value: scrollHeight,
  });
  Object.defineProperty(window, "innerHeight", {
    configurable: true,
    value: 800,
  });
}

describe("ScrollRestore", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    setBody(0);
    window.scrollTo = vi.fn() as never;
  });

  it("does nothing when no pending restore", () => {
    const sr = new ScrollRestore();
    sr.attemptRestore();
    expect(window.scrollTo).not.toHaveBeenCalled();
  });

  it("does not scroll until target element exists or scrollHeight satisfies", () => {
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    sr.attemptRestore();
    expect(window.scrollTo).not.toHaveBeenCalled();
  });

  it("scrolls when scrollHeight satisfies (no target element)", () => {
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: null });
    setBody(2400);
    const ok = sr.attemptRestore();
    expect(ok).toBe(true);
    expect(window.scrollTo).toHaveBeenCalledWith(0, 1500);
  });

  it("scrolls when target element exists even before scrollHeight grows", () => {
    document.body.innerHTML = `<div data-media-id="m1"></div>`;
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    const ok = sr.attemptRestore();
    expect(ok).toBe(true);
    expect(window.scrollTo).toHaveBeenCalledWith(0, 1500);
  });

  it("clears pending after success", () => {
    document.body.innerHTML = `<div data-media-id="m1"></div>`;
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    sr.attemptRestore();
    (window.scrollTo as unknown as ReturnType<typeof vi.fn>).mockClear();
    sr.attemptRestore();
    expect(window.scrollTo).not.toHaveBeenCalled();
  });

  it("bails to partial Y after retry cap", () => {
    const sr = new ScrollRestore({ maxAttempts: 3 });
    sr.markPending({ scrollY: 5000, mediaId: "missing" });
    setBody(1000);
    sr.attemptRestore();
    sr.attemptRestore();
    sr.attemptRestore();
    const ok = sr.attemptRestore();
    expect(ok).toBe(true);
    expect(window.scrollTo).toHaveBeenCalledWith(0, 200); // 1000 - 800
  });
});

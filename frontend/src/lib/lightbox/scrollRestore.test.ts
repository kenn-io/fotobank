import { describe, it, expect, vi, beforeEach } from "vitest";
import { ScrollRestore } from "./scrollRestore.svelte";

function mountMain(opts: { scrollHeight?: number; clientHeight?: number; html?: string } = {}) {
  const main = document.createElement("section");
  main.className = "main";
  main.innerHTML = opts.html ?? "";
  document.body.appendChild(main);
  Object.defineProperty(main, "scrollHeight", {
    configurable: true,
    value: opts.scrollHeight ?? 0,
  });
  Object.defineProperty(main, "clientHeight", {
    configurable: true,
    value: opts.clientHeight ?? 800,
  });
  main.scrollTo = vi.fn() as never;
  return main;
}

describe("ScrollRestore", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("does nothing when no pending restore", () => {
    const main = mountMain();
    const sr = new ScrollRestore();
    sr.attemptRestore();
    expect(main.scrollTo).not.toHaveBeenCalled();
  });

  it("does not scroll until target element exists or scrollHeight satisfies", () => {
    const main = mountMain();
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    sr.attemptRestore();
    expect(main.scrollTo).not.toHaveBeenCalled();
  });

  it("scrolls when scrollHeight satisfies (no target element)", () => {
    const main = mountMain({ scrollHeight: 2400 });
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: null });
    const ok = sr.attemptRestore();
    expect(ok).toBe(true);
    expect(main.scrollTo).toHaveBeenCalledWith(0, 1500);
  });

  it("scrolls when target element exists even before scrollHeight grows", () => {
    const main = mountMain({ html: `<div data-media-id="m1"></div>` });
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    const ok = sr.attemptRestore();
    expect(ok).toBe(true);
    expect(main.scrollTo).toHaveBeenCalledWith(0, 1500);
  });

  it("targets the .main scroll container, not window/body", () => {
    const main = mountMain({ scrollHeight: 2400 });
    const sr = new ScrollRestore();
    const winScrollTo = vi.fn();
    window.scrollTo = winScrollTo as never;
    sr.markPending({ scrollY: 1500, mediaId: null });
    sr.attemptRestore();
    expect(main.scrollTo).toHaveBeenCalledWith(0, 1500);
    expect(winScrollTo).not.toHaveBeenCalled();
  });

  it("clears pending after success", () => {
    const main = mountMain({ html: `<div data-media-id="m1"></div>` });
    const sr = new ScrollRestore();
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    sr.attemptRestore();
    (main.scrollTo as unknown as ReturnType<typeof vi.fn>).mockClear();
    sr.attemptRestore();
    expect(main.scrollTo).not.toHaveBeenCalled();
  });

  it("bails to partial Y after retry cap", () => {
    const main = mountMain({ scrollHeight: 1000 });
    const sr = new ScrollRestore({ maxAttempts: 3 });
    sr.markPending({ scrollY: 5000, mediaId: "missing" });
    sr.attemptRestore(); // attempt 1: not yet capped
    sr.attemptRestore(); // attempt 2: not yet capped
    const ok = sr.attemptRestore(); // attempt 3: cap hit → bails to partial Y
    expect(ok).toBe(true);
    expect(main.scrollTo).toHaveBeenCalledWith(0, 200); // 1000 - 800
  });

  it("counts retries while .main is missing and bails at cap", () => {
    // No .main mounted — every attempt is a retry. Cap = 2 here.
    const sr = new ScrollRestore({ maxAttempts: 2 });
    sr.markPending({ scrollY: 1500, mediaId: "m1" });
    expect(sr.attemptRestore()).toBe(false);
    // Cap reached on second call; pending cleared even though no scroll happened.
    expect(sr.attemptRestore()).toBe(true);
    expect(sr.isPending()).toBe(false);
  });

  it("isPending reflects markPending / clear", () => {
    mountMain({ scrollHeight: 1000 });
    const sr = new ScrollRestore();
    expect(sr.isPending()).toBe(false);
    sr.markPending({ scrollY: 100, mediaId: null });
    expect(sr.isPending()).toBe(true);
    sr.attemptRestore();
    expect(sr.isPending()).toBe(false);
  });

  it("respects custom containerSelector", () => {
    const el = document.createElement("div");
    el.id = "custom";
    Object.defineProperty(el, "scrollHeight", { configurable: true, value: 2400 });
    Object.defineProperty(el, "clientHeight", { configurable: true, value: 800 });
    el.scrollTo = vi.fn() as never;
    document.body.appendChild(el);
    const sr = new ScrollRestore({ containerSelector: "#custom" });
    sr.markPending({ scrollY: 1500, mediaId: null });
    sr.attemptRestore();
    expect(el.scrollTo).toHaveBeenCalledWith(0, 1500);
  });
});

import { describe, it, expect } from "vitest";
import { computeNav } from "./lightboxNav.svelte";

describe("computeNav", () => {
  it("returns -1 / null when active id not in navIds", () => {
    const r = computeNav(["a", "b", "c"], "missing");
    expect(r).toEqual({ index: -1, prevId: null, nextId: null, hasPrev: false, hasNext: false });
  });

  it("returns prev/next at middle position", () => {
    const r = computeNav(["a", "b", "c"], "b");
    expect(r).toEqual({ index: 1, prevId: "a", nextId: "c", hasPrev: true, hasNext: true });
  });

  it("returns null prev at first position", () => {
    const r = computeNav(["a", "b", "c"], "a");
    expect(r.prevId).toBeNull();
    expect(r.nextId).toBe("b");
    expect(r.hasPrev).toBe(false);
    expect(r.hasNext).toBe(true);
  });

  it("returns null next at last position", () => {
    const r = computeNav(["a", "b", "c"], "c");
    expect(r.prevId).toBe("b");
    expect(r.nextId).toBeNull();
    expect(r.hasPrev).toBe(true);
    expect(r.hasNext).toBe(false);
  });

  it("single-item nav has no prev/next", () => {
    const r = computeNav(["a"], "a");
    expect(r).toEqual({ index: 0, prevId: null, nextId: null, hasPrev: false, hasNext: false });
  });

  it("empty navIds returns -1", () => {
    const r = computeNav([], "x");
    expect(r.index).toBe(-1);
  });
});

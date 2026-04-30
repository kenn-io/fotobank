import { describe, it, expect } from "vitest";
import { LightboxSessionStore, type LightboxSource } from "./lightboxSession.svelte";

const lib: LightboxSource = { kind: "library" };

describe("LightboxSessionStore", () => {
  it("starts empty", () => {
    const s = new LightboxSessionStore();
    expect(s.snapshot).toBeNull();
  });

  it("open() captures the snapshot", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a", "b", "c"],
      selected: false,
      scrollY: 1234,
      returnFocusMediaId: "a",
      returnHref: "/library",
    });
    expect(s.snapshot?.navIds).toEqual(["a", "b", "c"]);
    expect(s.snapshot?.scrollY).toBe(1234);
  });

  it("close() clears the snapshot", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: "a",
      returnHref: "/library",
    });
    s.close();
    expect(s.snapshot).toBeNull();
  });

  it("clearScroll() zeroes scrollY without dropping the snapshot", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a"],
      selected: false,
      scrollY: 1234,
      returnFocusMediaId: "a",
      returnHref: "/library",
    });
    s.clearScroll();
    expect(s.snapshot?.scrollY).toBe(0);
    expect(s.snapshot?.navIds).toEqual(["a"]);
  });

  it("clearReturnFocus() clears returnFocusMediaId only", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a"],
      selected: false,
      scrollY: 100,
      returnFocusMediaId: "a",
      returnHref: "/library",
    });
    s.clearReturnFocus();
    expect(s.snapshot?.returnFocusMediaId).toBeNull();
    expect(s.snapshot?.scrollY).toBe(100);
  });

  it("removeIds() removes ids from navIds", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a", "b", "c", "d"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: null,
      returnHref: "/library",
    });
    s.removeIds(["b", "d", "missing"]);
    expect(s.snapshot?.navIds).toEqual(["a", "c"]);
  });

  it("removeIds() on no-op input leaves navIds untouched", () => {
    const s = new LightboxSessionStore();
    s.open({
      source: lib,
      navIds: ["a", "b"],
      selected: false,
      scrollY: 0,
      returnFocusMediaId: null,
      returnHref: "/library",
    });
    s.removeIds([]);
    expect(s.snapshot?.navIds).toEqual(["a", "b"]);
  });
});

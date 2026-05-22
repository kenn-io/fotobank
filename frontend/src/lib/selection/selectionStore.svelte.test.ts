// frontend/src/lib/selection/selectionStore.svelte.test.ts
import { describe, it, expect, beforeEach } from "vitest";
import { flushSync } from "svelte";
import { SelectionStore } from "./selectionStore.svelte";

describe("SelectionStore", () => {
  it("toggle adds and removes ids", () => {
    const s = new SelectionStore();
    s.toggle("a"); s.toggle("b"); s.toggle("a");
    expect(s.ids).toEqual(new Set(["b"]));
  });

  it("range selects contiguous slice given an ordering", () => {
    const s = new SelectionStore();
    const order = ["a", "b", "c", "d", "e"];
    s.toggle("b");
    s.range("d", order);
    expect([...s.ids].sort()).toEqual(["b", "c", "d"]);
  });

  it("clear empties selection", () => {
    const s = new SelectionStore();
    s.toggle("a"); s.toggle("b");
    s.clear();
    expect(s.ids.size).toBe(0);
  });
});

describe("SelectionStore.addAll", () => {
  let s: SelectionStore;
  beforeEach(() => { s = new SelectionStore(); });

  it("adds every id and sets lastAnchor to the last id in the iterable", () => {
    s.addAll(["a", "b", "c"]);
    expect(s.ids.has("a")).toBe(true);
    expect(s.ids.has("b")).toBe(true);
    expect(s.ids.has("c")).toBe(true);
    expect(s.ids.size).toBe(3);
    expect(s.lastAnchor).toBe("c");
  });

  it("is a no-op for an empty iterable; lastAnchor unchanged", () => {
    s.toggle("seed");
    s.addAll([]);
    expect(s.lastAnchor).toBe("seed");
    expect(s.ids.size).toBe(1);
  });

  it("merges with existing selection without duplicates", () => {
    s.addAll(["a", "b"]);
    s.addAll(["b", "c"]);
    expect(s.ids.size).toBe(3);
  });
});

describe("SelectionStore.removeAll", () => {
  let s: SelectionStore;
  beforeEach(() => {
    s = new SelectionStore();
    s.addAll(["a", "b", "c", "d"]);
  });

  it("removes every id and leaves lastAnchor untouched", () => {
    const anchor = s.lastAnchor;
    s.removeAll(["b", "c"]);
    expect(s.ids.has("a")).toBe(true);
    expect(s.ids.has("b")).toBe(false);
    expect(s.ids.has("c")).toBe(false);
    expect(s.ids.has("d")).toBe(true);
    expect(s.lastAnchor).toBe(anchor);
  });

  it("is tolerant of unknown ids", () => {
    s.removeAll(["z"]);
    expect(s.ids.size).toBe(4);
  });
});

describe("SelectionStore.hasAll", () => {
  let s: SelectionStore;
  beforeEach(() => {
    s = new SelectionStore();
    s.addAll(["a", "b", "c"]);
  });

  it("returns true when every id is present", () => {
    expect(s.hasAll(["a", "b"])).toBe(true);
    expect(s.hasAll(["a", "b", "c"])).toBe(true);
  });

  it("returns false when any id is missing", () => {
    expect(s.hasAll(["a", "z"])).toBe(false);
  });

  it("returns false for empty input (so empty chunks don't render Deselect)", () => {
    expect(s.hasAll([])).toBe(false);
  });
});

describe("SelectionStore reactivity (regression for direct-mutation bug)", () => {
  it("$derived(hasAll(...)) updates after addAll and removeAll", () => {
    // Svelte 5's $state does not auto-proxy built-in Map/Set, so direct
    // .add()/.delete() on this.ids would not trigger $derived updates.
    // addAll/removeAll must reassign this.ids to a fresh Set; this test
    // exercises the $derived path that components like GroupSelectButton
    // depend on (design §13.2).
    const s = new SelectionStore();
    let observed: boolean | undefined;
    const cleanup = $effect.root(() => {
      const v = $derived(s.hasAll(["a", "b"]));
      $effect(() => { observed = v; });
    });
    flushSync();
    expect(observed).toBe(false);

    s.addAll(["a", "b"]);
    flushSync();
    expect(observed).toBe(true);

    s.removeAll(["a"]);
    flushSync();
    expect(observed).toBe(false);

    cleanup();
  });
});

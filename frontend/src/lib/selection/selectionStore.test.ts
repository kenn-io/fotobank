// frontend/src/lib/selection/selectionStore.test.ts
import { describe, it, expect } from "vitest";
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

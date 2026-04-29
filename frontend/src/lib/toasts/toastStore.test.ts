import { describe, it, expect } from "vitest";
import { ToastStore } from "./toastStore.svelte";

describe("ToastStore.push", () => {
  it("adds an item with a unique id", () => {
    const store = new ToastStore();
    const id1 = store.push({ message: "Hello" });
    const id2 = store.push({ message: "World" });
    expect(store.items).toHaveLength(2);
    expect(id1).not.toBe(id2);
    expect(store.items.at(0)?.message).toBe("Hello");
    expect(store.items.at(1)?.message).toBe("World");
  });

  it("returns the generated id", () => {
    const store = new ToastStore();
    const id = store.push({ message: "Test" });
    expect(typeof id).toBe("string");
    expect(id.length).toBeGreaterThan(0);
    expect(store.items.at(0)?.id).toBe(id);
  });

  it("stores optional fields (details, kind)", () => {
    const store = new ToastStore();
    store.push({ message: "Err", details: ["line 1", "line 2"], kind: "error" });
    expect(store.items.at(0)?.details).toEqual(["line 1", "line 2"]);
    expect(store.items.at(0)?.kind).toBe("error");
  });
});

describe("ToastStore.dismiss", () => {
  it("removes the item with the given id", () => {
    const store = new ToastStore();
    const id = store.push({ message: "Remove me" });
    store.push({ message: "Keep me" });
    store.dismiss(id);
    expect(store.items).toHaveLength(1);
    expect(store.items.at(0)?.message).toBe("Keep me");
  });

  it("is a no-op for an unknown id", () => {
    const store = new ToastStore();
    store.push({ message: "A" });
    store.dismiss("nonexistent");
    expect(store.items).toHaveLength(1);
  });
});

describe("ToastStore.clear", () => {
  it("empties the list", () => {
    const store = new ToastStore();
    store.push({ message: "A" });
    store.push({ message: "B" });
    store.clear();
    expect(store.items).toHaveLength(0);
  });

  it("is a no-op when already empty", () => {
    const store = new ToastStore();
    store.clear();
    expect(store.items).toHaveLength(0);
  });
});

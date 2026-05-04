import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import FacetList from "./FacetList.svelte";

describe("FacetList", () => {
  const items = [
    { value: "Sony A7R IV", count: 845, selected: false },
    { value: "iPhone 15 Pro", count: 302, selected: true },
    { value: "Canon EOS R5", count: 100, selected: false },
  ];

  it("renders one row per item with count", () => {
    const { getByText } = render(FacetList, { items, onToggle: () => {} });
    expect(getByText("Sony A7R IV")).toBeTruthy();
    expect(getByText("845")).toBeTruthy();
  });

  it("emits toggle on row click", async () => {
    const onToggle = vi.fn();
    const { getByText } = render(FacetList, { items, onToggle });
    await fireEvent.click(getByText("Sony A7R IV"));
    expect(onToggle).toHaveBeenCalledWith("Sony A7R IV");
  });

  it("shows search input only when items > 8", () => {
    const big = Array.from({ length: 9 }, (_, i) => ({
      value: `V${i}`,
      count: i,
      selected: false,
    }));
    const { queryByPlaceholderText } = render(FacetList, {
      items,
      onToggle: () => {},
      searchPlaceholder: "Search…",
    });
    expect(queryByPlaceholderText("Search…")).toBeNull();
    const big2 = render(FacetList, {
      items: big,
      onToggle: () => {},
      searchPlaceholder: "Search…",
    });
    expect(big2.queryByPlaceholderText("Search…")).toBeTruthy();
  });

  it("substring search filters items", async () => {
    const big = Array.from({ length: 10 }, (_, i) => ({
      value: `Camera-${i}`,
      count: i,
      selected: false,
    }));
    const { getByPlaceholderText, queryByText } = render(FacetList, {
      items: big,
      onToggle: () => {},
      searchPlaceholder: "Search…",
    });
    const input = getByPlaceholderText("Search…");
    await fireEvent.input(input, { target: { value: "ra-3" } });
    expect(queryByText("Camera-3")).toBeTruthy();
    expect(queryByText("Camera-2")).toBeNull();
  });

  it("ignores stale query when items shrink below threshold", async () => {
    // The search input only renders when items > 8. If the user types a
    // query against 10 items, then a filter elsewhere narrows the items
    // to 5, the search input disappears with no way to clear the query.
    // Without the gate the rendered list would still apply the stale
    // filter — a regression that hides matching rows from the user.
    const big = Array.from({ length: 10 }, (_, i) => ({
      value: `Camera-${i}`,
      count: i,
      selected: false,
    }));
    const { getByPlaceholderText, queryByText, rerender } = render(FacetList, {
      items: big,
      onToggle: () => {},
      searchPlaceholder: "Search…",
    });
    const input = getByPlaceholderText("Search…");
    await fireEvent.input(input, { target: { value: "Camera-3" } });
    expect(queryByText("Camera-3")).toBeTruthy();
    // Now narrow items to 3 — search disappears, but the surviving rows
    // must all render regardless of the prior query.
    const small = big.slice(0, 3);
    await rerender({ items: small, onToggle: () => {}, searchPlaceholder: "Search…" });
    expect(queryByText("Camera-0")).toBeTruthy();
    expect(queryByText("Camera-1")).toBeTruthy();
    expect(queryByText("Camera-2")).toBeTruthy();
  });

  it("rows expose checkbox semantics with aria-checked", () => {
    // Visually the rows behave like checkboxes (check glyph + toggle on
    // click), so screen readers must see the same affordance. Without
    // role=checkbox + aria-checked, assistive tech would read each row
    // as a plain button with no indication of selection state.
    const { container } = render(FacetList, { items, onToggle: () => {} });
    const rows = container.querySelectorAll('[role="checkbox"]');
    expect(rows.length).toBe(items.length);
    expect(rows[0]?.getAttribute("aria-checked")).toBe("false");
    expect(rows[1]?.getAttribute("aria-checked")).toBe("true");
  });
});

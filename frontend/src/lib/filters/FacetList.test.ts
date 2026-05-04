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
});

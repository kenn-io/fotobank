import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import FilterChipStrip from "./FilterChipStrip.svelte";
import type { ActiveFilters } from "./activeFilters";

describe("FilterChipStrip", () => {
  const empty: ActiveFilters = {
    cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
  };

  it("renders nothing when no filters are active", () => {
    const { container } = render(FilterChipStrip, {
      filters: empty, tagLabels: {}, onChange: () => {},
    });
    expect(container.querySelector(".chip")).toBeNull();
  });

  it("renders a chip per camera + clicking × removes it", async () => {
    const onChange = vi.fn();
    const filters: ActiveFilters = { ...empty, cameras: ["Sony", "Canon"] };
    const { container } = render(FilterChipStrip, {
      filters, tagLabels: {}, onChange,
    });
    expect(container.querySelectorAll(".chip").length).toBe(2);
    const xButtons = container.querySelectorAll(".chip-x");
    await fireEvent.click(xButtons[0] as Element);
    expect(onChange).toHaveBeenCalledWith({ ...empty, cameras: ["Canon"] });
  });

  it("Clear all only when ≥2 chips", () => {
    const a = render(FilterChipStrip, {
      filters: { ...empty, cameras: ["Sony"] }, tagLabels: {}, onChange: () => {},
    });
    expect(a.queryByText("Clear all")).toBeNull();
    const b = render(FilterChipStrip, {
      filters: { ...empty, cameras: ["Sony", "Canon"] }, tagLabels: {}, onChange: () => {},
    });
    expect(b.queryByText("Clear all")).toBeTruthy();
  });

  it("tag chips render display label not key", () => {
    const { getByText } = render(FilterChipStrip, {
      filters: { ...empty, tagKeys: ["dog"] },
      tagLabels: { dog: "Dog" }, onChange: () => {},
    });
    expect(getByText(/Dog/)).toBeTruthy();
  });
});

import { describe, it, expect, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import FacetSection from "./FacetSection.svelte";

describe("FacetSection", () => {
  const items = [
    { value: "Sony", count: 5, selected: true },
    { value: "Canon", count: 3, selected: false },
  ];

  beforeEach(() => {
    localStorage.clear();
  });

  it("renders the label and total count", () => {
    const { getByText } = render(FacetSection, {
      label: "Cameras",
      totalCount: 8,
      items,
      onToggle: () => {},
      storageKey: "facet:test:cameras",
      expandedDefault: true,
    });
    expect(getByText("Cameras")).toBeTruthy();
    expect(getByText("8")).toBeTruthy();
  });

  it("collapsed state hides the FacetList body", () => {
    localStorage.setItem("facet:test:cameras", "collapsed");
    const { queryByText } = render(FacetSection, {
      label: "Cameras",
      totalCount: 8,
      items,
      onToggle: () => {},
      storageKey: "facet:test:cameras",
      expandedDefault: true,
    });
    expect(queryByText("Sony")).toBeNull();
  });

  it("active-count badge shows when collapsed and items selected", () => {
    localStorage.setItem("facet:test:cameras", "collapsed");
    const { getByText } = render(FacetSection, {
      label: "Cameras",
      totalCount: 8,
      items,
      onToggle: () => {},
      storageKey: "facet:test:cameras",
      expandedDefault: true,
    });
    expect(getByText("1")).toBeTruthy(); // 1 selected
  });

  it("toggling header flips collapse state and persists", async () => {
    const { getByRole } = render(FacetSection, {
      label: "Cameras",
      totalCount: 8,
      items,
      onToggle: () => {},
      storageKey: "facet:test:cameras",
      expandedDefault: true,
    });
    const header = getByRole("button", { name: /Cameras/ });
    await fireEvent.click(header);
    expect(localStorage.getItem("facet:test:cameras")).toBe("collapsed");
  });
});

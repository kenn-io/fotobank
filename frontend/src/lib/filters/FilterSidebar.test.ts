import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import FilterSidebar from "./FilterSidebar.svelte";
import type { FacetsResponse } from "./facetsStore.svelte";
import type { ActiveFilters } from "./activeFilters";

const empty: ActiveFilters = {
  cameras: [], lenses: [], tagKeys: [], hasGps: null, mediaType: null,
};
const fakeResp: FacetsResponse = {
  cameras: [{ value: "Sony A7R IV", count: 5 }],
  lenses: [{ value: "FE 24-70mm F2.8 GM", count: 3 }],
  tags: [{ key: "dog", label: "Dog", count: 2 }],
  places: { with_gps: 4, without_gps: 1 },
  media_types: [{ value: "photo", count: 8 }, { value: "video", count: 1 }],
};

describe("FilterSidebar", () => {
  it("renders all five sections on /library", () => {
    const { getByText } = render(FilterSidebar, {
      route: "library", filters: empty, response: fakeResp, onChange: () => {},
    });
    expect(getByText("Cameras")).toBeTruthy();
    expect(getByText("Lenses")).toBeTruthy();
    expect(getByText("Tags")).toBeTruthy();
    expect(getByText("Places")).toBeTruthy();
    expect(getByText("Media Type")).toBeTruthy();
  });

  it("hides Places on /map", () => {
    const { queryByText } = render(FilterSidebar, {
      route: "map", filters: empty, response: fakeResp, onChange: () => {},
    });
    expect(queryByText("Places")).toBeNull();
  });

  it("emits onChange with the toggled filter set", async () => {
    const onChange = vi.fn();
    const { getByText } = render(FilterSidebar, {
      route: "library", filters: empty, response: fakeResp, onChange,
    });
    await fireEvent.click(getByText("Sony A7R IV"));
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({
      cameras: ["Sony A7R IV"],
    }));
  });
});

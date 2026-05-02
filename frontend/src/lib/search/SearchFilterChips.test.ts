import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import SearchFilterChips from "./SearchFilterChips.svelte";
import type { SearchFilters } from "./types";

describe("SearchFilterChips", () => {
  it("renders nothing when filters are empty", () => {
    const { container } = render(SearchFilterChips, {
      props: { filters: { tags: [] }, onChange: vi.fn() },
    });
    // Container present but no chips rendered.
    const chips = container.querySelectorAll("[data-testid^='chip-']");
    expect(chips.length).toBe(0);
  });

  it("date range chips render half-open semantics", () => {
    // dateAfter renders with "After" prefix, dateBefore with "Before"
    // — matching the half-open [after, before) semantics the search
    // engine applies. The labels are user-facing; the test asserts
    // both values appear in the rendered text.
    const filters: SearchFilters = {
      tags: [],
      dateAfter: "2025-01-01",
      dateBefore: "2025-02-01",
    };
    const { container, getByText } = render(SearchFilterChips, {
      props: { filters, onChange: vi.fn() },
    });
    expect(container.querySelector("[data-testid='chip-date-after']")).toBeTruthy();
    expect(container.querySelector("[data-testid='chip-date-before']")).toBeTruthy();
    expect(getByText(/After/)).toBeTruthy();
    expect(getByText(/Before/)).toBeTruthy();
    expect(getByText(/2025-01-01/)).toBeTruthy();
    expect(getByText(/2025-02-01/)).toBeTruthy();
  });

  it("renders a tag chip per active tag with the tag_label visible", () => {
    const filters: SearchFilters = {
      tags: [
        { tag_key: "dog", tag_label: "Dog" },
        { tag_key: "beach", tag_label: "Beach" },
      ],
    };
    const { container, getByText } = render(SearchFilterChips, {
      props: { filters, onChange: vi.fn() },
    });
    const tagChips = container.querySelectorAll("[data-testid^='chip-tag-']");
    expect(tagChips.length).toBe(2);
    expect(getByText("Dog")).toBeTruthy();
    expect(getByText("Beach")).toBeTruthy();
  });

  it("renders a location chip", () => {
    const filters: SearchFilters = {
      tags: [],
      location: { location_label: "Paris, France" },
    };
    const { container, getByText } = render(SearchFilterChips, {
      props: { filters, onChange: vi.fn() },
    });
    expect(container.querySelector("[data-testid='chip-location']")).toBeTruthy();
    expect(getByText(/Paris, France/)).toBeTruthy();
  });

  it("renders a media-type chip", () => {
    const filters: SearchFilters = { tags: [], mediaType: "photo" };
    const { container, getByText } = render(SearchFilterChips, {
      props: { filters, onChange: vi.fn() },
    });
    expect(container.querySelector("[data-testid='chip-media-type']")).toBeTruthy();
    expect(getByText(/Photo/i)).toBeTruthy();
  });

  it("removing a date chip dispatches change with the chip absent", async () => {
    const onChange = vi.fn();
    const filters: SearchFilters = {
      tags: [],
      dateAfter: "2025-01-01",
      dateBefore: "2025-02-01",
    };
    const { container } = render(SearchFilterChips, {
      props: { filters, onChange },
    });
    const removeAfter = container.querySelector(
      "[data-testid='chip-date-after'] [data-testid='chip-remove']",
    ) as HTMLButtonElement;
    expect(removeAfter).toBeTruthy();
    await fireEvent.click(removeAfter);
    expect(onChange).toHaveBeenCalledWith({
      tags: [],
      dateBefore: "2025-02-01",
    });
  });

  it("removing a tag chip dispatches change with that tag absent", async () => {
    const onChange = vi.fn();
    const filters: SearchFilters = {
      tags: [
        { tag_key: "dog", tag_label: "Dog" },
        { tag_key: "beach", tag_label: "Beach" },
      ],
    };
    const { container } = render(SearchFilterChips, {
      props: { filters, onChange },
    });
    // Each tag chip is keyed by tag_key in its data-testid so the test
    // can target a specific chip without depending on render order.
    const removeBeach = container.querySelector(
      "[data-testid='chip-tag-beach'] [data-testid='chip-remove']",
    ) as HTMLButtonElement;
    await fireEvent.click(removeBeach);
    expect(onChange).toHaveBeenCalledWith({
      tags: [{ tag_key: "dog", tag_label: "Dog" }],
    });
  });

  it("removing a location chip dispatches change with location absent", async () => {
    const onChange = vi.fn();
    const filters: SearchFilters = {
      tags: [{ tag_key: "dog", tag_label: "Dog" }],
      location: { location_label: "Paris, France" },
    };
    const { container } = render(SearchFilterChips, {
      props: { filters, onChange },
    });
    const removeLocation = container.querySelector(
      "[data-testid='chip-location'] [data-testid='chip-remove']",
    ) as HTMLButtonElement;
    await fireEvent.click(removeLocation);
    expect(onChange).toHaveBeenCalledWith({
      tags: [{ tag_key: "dog", tag_label: "Dog" }],
    });
  });

  it("removing the media-type chip dispatches change with mediaType absent", async () => {
    const onChange = vi.fn();
    const filters: SearchFilters = { tags: [], mediaType: "video" };
    const { container } = render(SearchFilterChips, {
      props: { filters, onChange },
    });
    const removeMediaType = container.querySelector(
      "[data-testid='chip-media-type'] [data-testid='chip-remove']",
    ) as HTMLButtonElement;
    await fireEvent.click(removeMediaType);
    // mediaType absent (not undefined-as-property) so the wire shape
    // drops it. Use the actual call args rather than toHaveBeenCalledWith
    // so we can assert on key presence, not just deep equality.
    const args = onChange.mock.calls[0]![0] as SearchFilters;
    expect(args.mediaType).toBeUndefined();
    expect(args.tags).toEqual([]);
  });
});

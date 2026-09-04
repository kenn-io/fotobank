import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/svelte";
import SearchFiltersPopover from "./SearchFiltersPopover.svelte";
import type { SearchClient } from "./client";
import type { SearchFilters } from "./types";

// makeClient builds a SearchClient stub whose autocomplete responses
// can be tuned per test. The .search method is unused by the popover
// but kept on the surface so the type fits SearchClient.
function makeClient(opts: {
  tags?: { key: string; label: string; count: number }[];
  locations?: { label: string; count: number }[];
} = {}): SearchClient {
  return {
    search: vi.fn().mockResolvedValue({
      results: [],
      next_cursor: null,
      has_more: false,
      effective_sort: "relevance",
      embedding_completeness: 0,
      semantic_unavailable: false,
      semantic_unavailable_reason: "",
    }),
    autocompleteTags: vi.fn().mockResolvedValue({ tags: opts.tags ?? [] }),
    autocompleteLocations: vi.fn().mockResolvedValue({ locations: opts.locations ?? [] }),
  };
}

// emptyFilters mirrors searchStore.emptyFilters — the canonical
// no-filters-set shape.
function emptyFilters(): SearchFilters {
  return { tags: [], cameras: [], lenses: [], facetTagKeys: [] };
}

describe("SearchFiltersPopover", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("date inputs commit dateAfter / dateBefore through onChange", async () => {
    // Type into both date inputs and verify onChange fires with the
    // half-open semantics the backend expects (date_after inclusive,
    // date_before exclusive — the popover is just a transport here).
    // The popover holds no internal date state; the parent updates
    // `filters` between calls in production. We mirror that by
    // re-rendering with the partially-applied filters before the
    // second input event.
    const onChange = vi.fn();
    const client = makeClient();
    const { container, rerender } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange, client },
    });

    const after = container.querySelector(
      "input[data-testid='search-filter-date-after']",
    ) as HTMLInputElement;
    expect(after).toBeTruthy();

    await fireEvent.input(after, { target: { value: "2025-01-01" } });
    expect(onChange).toHaveBeenLastCalledWith({
      tags: [],
      cameras: [],
      lenses: [],
      facetTagKeys: [],
      dateAfter: "2025-01-01",
    });

    // Production flow: parent applies onChange to the store, which
    // re-renders with the new filters. Mirror that by re-rendering
    // the popover with dateAfter set, then type into Before.
    await rerender({
      filters: {
        tags: [], cameras: [], lenses: [], facetTagKeys: [], dateAfter: "2025-01-01",
      },
      onChange,
      client,
    });
    const before = container.querySelector(
      "input[data-testid='search-filter-date-before']",
    ) as HTMLInputElement;
    expect(before).toBeTruthy();
    await fireEvent.input(before, { target: { value: "2025-02-01" } });
    expect(onChange).toHaveBeenLastCalledWith({
      tags: [],
      cameras: [],
      lenses: [],
      facetTagKeys: [],
      dateAfter: "2025-01-01",
      dateBefore: "2025-02-01",
    });
  });

  it("tag autocomplete commits a chip carrying tag_key", async () => {
    // Typing "do" with debounce → suggestion {key: "dog", label: "Dog"}
    // → click → onChange fires with filters.tags carrying tag_key from
    // the suggestion's `key` (NOT the typed input).
    const onChange = vi.fn();
    const client = makeClient({
      tags: [{ key: "dog", label: "Dog", count: 12 }],
    });
    const { container, findByText } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange, client },
    });

    const tagInput = container.querySelector(
      "input[data-testid='search-filter-tag-input']",
    ) as HTMLInputElement;
    expect(tagInput).toBeTruthy();

    await fireEvent.input(tagInput, { target: { value: "do" } });
    // Debounce: nothing fired yet.
    expect(client.autocompleteTags).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(200);
    expect(client.autocompleteTags).toHaveBeenCalledWith({ prefix: "do" });

    // Suggestion list renders after the promise resolves.
    const suggestion = await findByText("Dog");
    await fireEvent.click(suggestion);

    expect(onChange).toHaveBeenLastCalledWith({
      tags: [{ tag_key: "dog", tag_label: "Dog" }],
      cameras: [],
      lenses: [],
      facetTagKeys: [],
    });
    // Input clears after a successful add so the user can type the next.
    expect(tagInput.value).toBe("");
  });

  it("location autocomplete commits a chip carrying location_label", async () => {
    const onChange = vi.fn();
    const client = makeClient({
      locations: [{ label: "Paris, France", count: 7 }],
    });
    const { container, findByText } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange, client },
    });

    const locInput = container.querySelector(
      "input[data-testid='search-filter-location-input']",
    ) as HTMLInputElement;
    expect(locInput).toBeTruthy();

    await fireEvent.input(locInput, { target: { value: "par" } });
    await vi.advanceTimersByTimeAsync(200);
    expect(client.autocompleteLocations).toHaveBeenCalledWith({ substring: "par" });

    const suggestion = await findByText("Paris, France");
    await fireEvent.click(suggestion);

    expect(onChange).toHaveBeenLastCalledWith({
      tags: [],
      cameras: [],
      lenses: [],
      facetTagKeys: [],
      location: { location_label: "Paris, France" },
    });
    expect(locInput.value).toBe("");
  });

  it("media-type segmented control sets photo|video|null", async () => {
    const onChange = vi.fn();
    const { getByRole, rerender } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange, client: makeClient() },
    });

    const photoBtn = getByRole("radio", { name: "Photos" });
    const videoBtn = getByRole("radio", { name: "Videos" });
    const allBtn = getByRole("radio", { name: "All" });

    await fireEvent.click(photoBtn);
    expect(onChange).toHaveBeenLastCalledWith({
      tags: [], cameras: [], lenses: [], facetTagKeys: [], mediaType: "photo",
    });

    // Re-render with the photo filter applied so the next click toggles
    // back through onChange. The component owns the visual selection
    // off the filters prop.
    await rerender({
      filters: {
        tags: [], cameras: [], lenses: [], facetTagKeys: [], mediaType: "photo",
      },
      onChange,
      client: makeClient(),
    });
    await fireEvent.click(videoBtn);
    expect(onChange).toHaveBeenLastCalledWith({
      tags: [], cameras: [], lenses: [], facetTagKeys: [], mediaType: "video",
    });

    await rerender({
      filters: {
        tags: [], cameras: [], lenses: [], facetTagKeys: [], mediaType: "video",
      },
      onChange,
      client: makeClient(),
    });
    await fireEvent.click(allBtn);
    // "All" clears mediaType — the field should be absent rather than
    // explicit undefined so the wire serializer drops it.
    const last = onChange.mock.calls[onChange.mock.calls.length - 1]![0] as SearchFilters;
    expect(last.mediaType).toBeUndefined();
    expect(last.tags).toEqual([]);
  });

  it("popover reflects pre-filled filter values from the filters prop", () => {
    // The "click chip to reopen popover" test from the plan reduces to
    // this in v1: the popover always renders inline, and its inputs
    // mirror the current filters prop. Re-mounting with filters that
    // carry dateAfter/dateBefore must show those values in the inputs.
    const filters: SearchFilters = {
      tags: [],
      cameras: [],
      lenses: [],
      facetTagKeys: [],
      dateAfter: "2025-01-01",
      dateBefore: "2025-02-01",
      mediaType: "photo",
    };
    const { container, getByRole } = render(SearchFiltersPopover, {
      props: { filters, onChange: vi.fn(), client: makeClient() },
    });
    const after = container.querySelector(
      "input[data-testid='search-filter-date-after']",
    ) as HTMLInputElement;
    const before = container.querySelector(
      "input[data-testid='search-filter-date-before']",
    ) as HTMLInputElement;
    const photoBtn = getByRole("radio", { name: "Photos" });
    expect(after.value).toBe("2025-01-01");
    expect(before.value).toBe("2025-02-01");
    // The active media-type option has aria-checked=true so callers
    // (and screen readers) can identify the selected value.
    expect(photoBtn.getAttribute("aria-checked")).toBe("true");
  });

  it("autocomplete debounces successive keystrokes within 200ms", async () => {
    // Typing three characters within the debounce window should result
    // in exactly one autocomplete call with the final value, not three
    // back-to-back calls.
    const client = makeClient({ tags: [{ key: "dog", label: "Dog", count: 1 }] });
    const { container } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange: vi.fn(), client },
    });
    const tagInput = container.querySelector(
      "input[data-testid='search-filter-tag-input']",
    ) as HTMLInputElement;

    await fireEvent.input(tagInput, { target: { value: "d" } });
    await fireEvent.input(tagInput, { target: { value: "do" } });
    await fireEvent.input(tagInput, { target: { value: "dog" } });

    // Nothing fired yet — debounce hasn't elapsed.
    expect(client.autocompleteTags).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(199);
    expect(client.autocompleteTags).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1);
    expect(client.autocompleteTags).toHaveBeenCalledTimes(1);
    expect(client.autocompleteTags).toHaveBeenCalledWith({ prefix: "dog" });
  });

  it("toggling include-hidden commits includeHidden=true through onChange", async () => {
    // The include-hidden checkbox is opt-in: checked → emit
    // includeHidden=true; unchecked → emit with the field removed so
    // the wire serializer drops it (the engine's default is exclusion).
    const onChange = vi.fn();
    const client = makeClient();
    const { getByRole, rerender } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange, client },
    });
    const toggle = getByRole("checkbox", {
      name: "Include hidden media",
    }) as HTMLInputElement;
    expect(toggle).toBeTruthy();
    expect(toggle.checked).toBe(false);

    await fireEvent.change(toggle, { target: { checked: true } });
    expect(onChange).toHaveBeenLastCalledWith({
      tags: [],
      cameras: [],
      lenses: [],
      facetTagKeys: [],
      includeHidden: true,
    });

    // Re-render with the flag applied so unchecking flows through.
    await rerender({
      filters: {
        tags: [], cameras: [], lenses: [], facetTagKeys: [], includeHidden: true,
      },
      onChange,
      client,
    });
    const toggle2 = getByRole("checkbox", {
      name: "Include hidden media",
    }) as HTMLInputElement;
    expect(toggle2.checked).toBe(true);
    await fireEvent.change(toggle2, { target: { checked: false } });
    const last = onChange.mock.calls[onChange.mock.calls.length - 1]![0] as SearchFilters;
    expect(last.includeHidden).toBeUndefined();
    expect(last.tags).toEqual([]);
  });

  it("moves to the next media type with the right arrow", async () => {
    const onChange = vi.fn();
    const { getByRole } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange, client: makeClient() },
    });

    await fireEvent.keyDown(getByRole("radio", { name: "All" }), {
      key: "ArrowRight",
    });

    expect(onChange).toHaveBeenCalledWith({
      tags: [],
      cameras: [],
      lenses: [],
      facetTagKeys: [],
      mediaType: "photo",
    });
  });

  it("clearing the tag input after typing cancels suggestions without an autocomplete call", async () => {
    const client = makeClient();
    const { container } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange: vi.fn(), client },
    });
    const tagInput = container.querySelector(
      "input[data-testid='search-filter-tag-input']",
    ) as HTMLInputElement;

    await fireEvent.input(tagInput, { target: { value: "d" } });
    await fireEvent.input(tagInput, { target: { value: "" } });
    await vi.advanceTimersByTimeAsync(500);
    // Empty input → no fetch.
    expect(client.autocompleteTags).not.toHaveBeenCalled();
    // Suggestions container present but empty.
    await waitFor(() => {
      const list = container.querySelector(
        "[data-testid='search-filter-tag-suggestions']",
      );
      expect(list).toBeTruthy();
      expect(list?.children.length ?? 0).toBe(0);
    });
  });

  it("ignores stale tag-autocomplete responses that land after the input is cleared", async () => {
    // Timing:
    //   t=0    type "do"
    //   t=200  debounce fires → autocompleteTags("do") dispatched, but
    //          the deferred promise is still pending
    //   t=205  user clears the input
    //   t=210  the deferred promise resolves with {dog}
    // Without per-input-change token bumping, the response lands and
    // overwrites the (correctly empty) suggestions for the cleared
    // input. The fix is to bump the fetch token synchronously on every
    // input change so the now-stale response is dropped.
    let resolveTag: (v: { tags: { key: string; label: string; count: number }[] }) => void = () => {};
    const tagPromise = new Promise<{ tags: { key: string; label: string; count: number }[] }>((res) => {
      resolveTag = res;
    });
    const client: SearchClient = {
      search: vi.fn().mockResolvedValue({
        results: [],
        next_cursor: null,
        has_more: false,
        effective_sort: "relevance",
        embedding_completeness: 0,
        semantic_unavailable: false,
        semantic_unavailable_reason: "",
      }),
      autocompleteTags: vi.fn(() => tagPromise),
      autocompleteLocations: vi.fn().mockResolvedValue({ locations: [] }),
    };
    const { container } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange: vi.fn(), client },
    });
    const tagInput = container.querySelector(
      "input[data-testid='search-filter-tag-input']",
    ) as HTMLInputElement;

    await fireEvent.input(tagInput, { target: { value: "do" } });
    await vi.advanceTimersByTimeAsync(200);
    // The dispatched request is in flight.
    expect(client.autocompleteTags).toHaveBeenCalledWith({ prefix: "do" });

    // Clear the input BEFORE the response resolves. The synchronous
    // token bump on input change must invalidate the in-flight request.
    await fireEvent.input(tagInput, { target: { value: "" } });

    // Now resolve the original "do" request — its response must be
    // dropped because the token has moved on.
    resolveTag({ tags: [{ key: "dog", label: "Dog", count: 5 }] });
    await vi.advanceTimersByTimeAsync(0);

    // Suggestions list stays empty for the cleared input.
    const list = container.querySelector(
      "[data-testid='search-filter-tag-suggestions']",
    );
    expect(list).toBeTruthy();
    expect(list?.children.length ?? 0).toBe(0);
  });

  it("ignores stale location-autocomplete responses that land after the input is cleared", async () => {
    let resolveLoc: (v: { locations: { label: string; count: number }[] }) => void = () => {};
    const locPromise = new Promise<{ locations: { label: string; count: number }[] }>((res) => {
      resolveLoc = res;
    });
    const client: SearchClient = {
      search: vi.fn().mockResolvedValue({
        results: [],
        next_cursor: null,
        has_more: false,
        effective_sort: "relevance",
        embedding_completeness: 0,
        semantic_unavailable: false,
        semantic_unavailable_reason: "",
      }),
      autocompleteTags: vi.fn().mockResolvedValue({ tags: [] }),
      autocompleteLocations: vi.fn(() => locPromise),
    };
    const { container } = render(SearchFiltersPopover, {
      props: { filters: emptyFilters(), onChange: vi.fn(), client },
    });
    const locInput = container.querySelector(
      "input[data-testid='search-filter-location-input']",
    ) as HTMLInputElement;

    await fireEvent.input(locInput, { target: { value: "par" } });
    await vi.advanceTimersByTimeAsync(200);
    expect(client.autocompleteLocations).toHaveBeenCalledWith({ substring: "par" });

    await fireEvent.input(locInput, { target: { value: "" } });

    resolveLoc({ locations: [{ label: "Paris, France", count: 7 }] });
    await vi.advanceTimersByTimeAsync(0);

    const list = container.querySelector(
      "[data-testid='search-filter-location-suggestions']",
    );
    expect(list).toBeTruthy();
    expect(list?.children.length ?? 0).toBe(0);
  });
});

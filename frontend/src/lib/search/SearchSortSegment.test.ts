import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/svelte";
import SearchSortSegment from "./SearchSortSegment.svelte";

// SearchSortSegment is a presentational segmented control over the
// SearchSort union ("relevance" | "newest" | "oldest"). The "selected"
// button reflects the *effective* sort the backend would apply: when
// sort="relevance" but the user has no query, the engine coerces to
// newest, so the segment shows Newest as selected to match. The user
// can still click Relevance — onChange fires with "relevance" and the
// store records the choice — but the visual selection stays Newest
// until a query is typed.
describe("SearchSortSegment", () => {
  it("default sort is Relevance when q != ''", () => {
    // A non-empty query means the engine honors the requested sort, so
    // sort="relevance" + q="trees" displays Relevance as selected.
    const { container } = render(SearchSortSegment, {
      props: { sort: "relevance", query: "trees", onChange: vi.fn() },
    });
    const selected = container.querySelector("button.selected");
    expect(selected).not.toBeNull();
    expect(selected!.textContent).toContain("Relevance");
  });

  it("default sort is Newest when q == ''", () => {
    // With an empty query the backend coerces relevance → newest. The
    // segment mirrors that coercion so the visual selection always
    // matches the applied sort.
    const { container } = render(SearchSortSegment, {
      props: { sort: "relevance", query: "", onChange: vi.fn() },
    });
    const selected = container.querySelector("button.selected");
    expect(selected).not.toBeNull();
    expect(selected!.textContent).toContain("Newest");
  });

  it("clicking a sort button fires onChange with the new value", async () => {
    // Drive the segment from the user side: click the Oldest button and
    // verify onChange receives "oldest". This is the equivalent of
    // store.setSort being invoked from the page-level wrapper.
    const onChange = vi.fn();
    const { getByText } = render(SearchSortSegment, {
      props: { sort: "relevance", query: "trees", onChange },
    });
    const oldest = getByText("Oldest");
    await fireEvent.click(oldest);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith("oldest");
  });
});

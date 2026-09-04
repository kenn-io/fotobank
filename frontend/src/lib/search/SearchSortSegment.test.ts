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
    const { getByRole } = render(SearchSortSegment, {
      props: { sort: "relevance", query: "trees", onChange: vi.fn() },
    });
    expect(
      getByRole("radio", { name: "Relevance" }).getAttribute("aria-checked"),
    ).toBe("true");
  });

  it("default sort is Newest when q == ''", () => {
    // With an empty query the backend coerces relevance → newest. The
    // segment mirrors that coercion so the visual selection always
    // matches the applied sort.
    const { getByRole } = render(SearchSortSegment, {
      props: { sort: "relevance", query: "", onChange: vi.fn() },
    });
    expect(
      getByRole("radio", { name: "Newest" }).getAttribute("aria-checked"),
    ).toBe("true");
  });

  it("clicking a sort button fires onChange with the new value", async () => {
    // Drive the segment from the user side: click the Oldest button and
    // verify onChange receives "oldest". This is the equivalent of
    // store.setSort being invoked from the page-level wrapper.
    const onChange = vi.fn();
    const { getByRole } = render(SearchSortSegment, {
      props: { sort: "relevance", query: "trees", onChange },
    });
    const oldest = getByRole("radio", { name: "Oldest" });
    await fireEvent.click(oldest);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith("oldest");
  });

  it("moves to the next sort with the right arrow", async () => {
    const onChange = vi.fn();
    const { getByRole } = render(SearchSortSegment, {
      props: { sort: "relevance", query: "trees", onChange },
    });

    await fireEvent.keyDown(getByRole("radio", { name: "Relevance" }), {
      key: "ArrowRight",
    });

    expect(onChange).toHaveBeenCalledWith("newest");
  });
});

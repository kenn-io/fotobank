import { describe, it, expect } from "vitest";
import { render } from "@testing-library/svelte";
import IndexingStatusPill from "./IndexingStatusPill.svelte";

// IndexingStatusPill renders a small inline badge that surfaces
// embedding_completeness from the search response. It hides when the
// library is fully indexed (completeness === 1) so the toolbar stays
// uncluttered for users in steady state. When embedded/eligible counts
// are supplied, the pill shows the absolute "X / Y indexed" form (the
// numbers carry more information than the percentage); otherwise it
// falls back to "Z% indexed" derived from completeness alone. The
// fotobank backend currently only returns embedding_completeness, so
// production callers will see the percentage form — the embedded /
// eligible inputs are wired for future expansion.

describe("IndexingStatusPill", () => {
  it("hides when completeness === 1", () => {
    const { container } = render(IndexingStatusPill, {
      props: { completeness: 1 },
    });
    // No <span class="pill"> renders when completeness is at the
    // ceiling. The {#if visible} guard short-circuits the entire
    // element so there is nothing in the DOM to assert against.
    expect(container.querySelector(".pill")).toBeNull();
  });

  it("renders 'X / Y indexed' when embedded and eligible are supplied", () => {
    // The plan's V1 acceptance test: with completeness=0.97 and the
    // explicit counts (2879, 2981), the pill shows the formatted
    // absolute counts using toLocaleString to add the thousands
    // separator. In the en-US locale the comma is the separator.
    const { getByText } = render(IndexingStatusPill, {
      props: { completeness: 0.97, embedded: 2879, eligible: 2981 },
    });
    expect(getByText(/2,879 \/ 2,981 indexed/)).toBeTruthy();
  });

  it("falls back to '% indexed' when only completeness is supplied", () => {
    // Production callers (Search.svelte) only pass completeness — the
    // backend's GET /api/v1/search response carries
    // embedding_completeness as a single float and no per-count
    // breakdown. The pill rounds to the nearest integer percent.
    const { getByText } = render(IndexingStatusPill, {
      props: { completeness: 0.42 },
    });
    expect(getByText(/42% indexed/)).toBeTruthy();
  });
});

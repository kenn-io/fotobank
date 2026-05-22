import { describe, it, expect } from "vitest";
import { render } from "@testing-library/svelte";
import DiagnosticsBadge from "./DiagnosticsBadge.svelte";
import type { SearchScoreComponents } from "./types";

// DiagnosticsBadge surfaces the per-result RRF fused score in a compact
// pill; hovering surfaces a tooltip that breaks the score into its
// per-signal pieces (BM25, Vector, with each signal's rank). The label
// shows the RRF only — the remaining detail is in the tooltip so the
// grid stays uncluttered when many badges are visible.

describe("DiagnosticsBadge", () => {
  it("renders the RRF score in the badge label", () => {
    const components: SearchScoreComponents = {
      rrf: 0.0156,
      bm25: 8.42,
      vector: 0.81,
      rank_bm25: 3,
      rank_vector: 7,
    };
    const { getByTestId } = render(DiagnosticsBadge, {
      props: { components },
    });
    const badge = getByTestId("diagnostics-badge");
    // The fused RRF score renders to 4 decimal places — RRF is a small
    // fraction (typically 0.01–0.05 with k=60) and 4 places keep
    // meaningful differences between hits visible.
    expect(badge.textContent?.trim()).toBe("0.0156");
  });

  it("encodes the per-signal breakdown in the title (tooltip)", () => {
    // The hover affordance is a native title attribute; the test
    // verifies the breakdown ends up in the tooltip text rather than
    // simulating a hover (jsdom doesn't render native tooltips). The
    // breakdown reads: RRF 0.0156, BM25 8.42 (rank 3), Vector 0.81
    // (rank 7) — each signal on its own line.
    const components: SearchScoreComponents = {
      rrf: 0.0156,
      bm25: 8.42,
      vector: 0.81,
      rank_bm25: 3,
      rank_vector: 7,
    };
    const { getByTestId } = render(DiagnosticsBadge, {
      props: { components },
    });
    const title = getByTestId("diagnostics-badge").getAttribute("title") ?? "";
    expect(title).toContain("RRF 0.0156");
    expect(title).toContain("BM25 8.42 (rank 3)");
    expect(title).toContain("Vector 0.81 (rank 7)");
  });

  it("renders an em-dash for null per-signal scores", () => {
    // Not every hit matches both signals — a doc could match BM25 but
    // be absent from the embedding generation. Null should surface as
    // an em-dash placeholder rather than "null" or 0.
    const components: SearchScoreComponents = {
      rrf: 0.0123,
      bm25: 5.0,
      vector: null,
      rank_bm25: 1,
      rank_vector: null,
    };
    const { getByTestId } = render(DiagnosticsBadge, {
      props: { components },
    });
    const title = getByTestId("diagnostics-badge").getAttribute("title") ?? "";
    expect(title).toContain("Vector —");
    // The rank suffix is only emitted when the rank is present; the
    // null-rank case must not append "(rank null)" to the line.
    expect(title).not.toContain("rank null");
  });
});

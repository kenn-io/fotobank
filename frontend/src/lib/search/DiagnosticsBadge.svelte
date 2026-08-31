<!-- frontend/src/lib/search/DiagnosticsBadge.svelte
     V2: small overlay badge that surfaces the per-result RRF score for
     a search hit when the user has AI Inspection enabled. The compact
     label shows the fused score; hovering surfaces a tooltip with the
     per-signal breakdown (BM25, Vector, plus their respective ranks).

     Why a separate component (vs. inlining in MediaCell): MediaCell is
     the universal grid cell, used by Library / Sessions / Hidden /
     Albums as well as Search. Pushing the diagnostics overlay into the
     cell would couple every grid renderer to a search-only concern.
     This component is mounted from the search page only, layered atop
     the cell via a wrapping <div data-testid="diagnostics-badge">. -->
<script lang="ts">
  import type { SearchScoreComponents } from "./types";

  let { components }: { components: SearchScoreComponents } = $props();

  // formatScore renders the RRF fused score with 4 decimal places. RRF
  // values are tiny (typically 0.01–0.05 range for k=60); a 4-place
  // float keeps a meaningful difference visible without being noisy.
  function formatScore(n: number): string {
    return n.toFixed(4);
  }

  // formatBM25 renders the BM25 raw score with 2 decimals. BM25 is
  // unbounded above (per FTS5's bm25() implementation it yields larger
  // negative magnitudes for stronger matches; we display the absolute
  // value of what the engine emits — the relative ordering is what
  // matters in the tooltip). Null when the doc didn't match BM25.
  function formatBM25(n: number | null): string {
    if (n === null) return "—";
    return n.toFixed(2);
  }

  // formatVector renders the cosine-distance score (already normalized
  // to a 0..1 similarity by the engine) with 2 decimals. Null when the
  // doc had no embedding under the active generation.
  function formatVector(n: number | null): string {
    if (n === null) return "—";
    return n.toFixed(2);
  }

  // formatRank renders the per-signal rank (1-based) when available.
  // Null when the doc didn't appear in that signal's top-K window — in
  // which case there is no rank to show.
  function formatRank(n: number | null): string {
    if (n === null) return "";
    return ` (rank ${n})`;
  }
</script>

<span
  class="diagnostics-badge"
  data-testid="diagnostics-badge"
  title={`RRF ${formatScore(components.rrf)}\nBM25 ${formatBM25(components.bm25)}${formatRank(components.rank_bm25)}\nVector ${formatVector(components.vector)}${formatRank(components.rank_vector)}`}
>
  {formatScore(components.rrf)}
</span>

<style>
  .diagnostics-badge {
    display: inline-flex;
    align-items: center;
    height: 18px;
    padding: 0 6px;
    border-radius: 4px;
    background: rgba(0, 0, 0, 0.65);
    color: var(--text-primary);
    font-size: 10px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-variant-numeric: tabular-nums;
    line-height: 1;
    white-space: nowrap;
    pointer-events: auto;
  }
</style>

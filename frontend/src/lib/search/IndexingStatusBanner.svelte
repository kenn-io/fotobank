<!-- TODO V1: replace with the banner that explains BM25-only fallback
     when semantic_unavailable is true (or completeness is below the
     threshold while the user has typed a query). T2 stub keeps the
     prop shape stable. -->
<script lang="ts">
  import type { SearchResponse } from "./types";

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  let {
    completeness,
    semanticUnavailable,
    reason,
    hasQuery,
  }: {
    completeness: number;
    semanticUnavailable: boolean;
    reason: SearchResponse["semantic_unavailable_reason"];
    hasQuery: boolean;
  } = $props();
</script>

<div class="indexing-status-banner" data-testid="indexing-status-banner-stub">
  <!-- V1 placeholder -->
</div>

<style>
  .indexing-status-banner { display: block; }
</style>

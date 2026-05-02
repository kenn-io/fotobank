<!-- TODO U1: replace with the chip strip that renders one chip per
     active filter and forwards x-button clicks back through onChange.
     T2 stub keeps the prop shape stable; see U1 in the search v1 plan. -->
<script lang="ts">
  import type { SearchFilters } from "./types";

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  let { filters, onChange }: {
    filters: SearchFilters;
    onChange?: (next: SearchFilters) => void;
  } = $props();
</script>

<div class="search-filter-chips" data-testid="search-filter-chips-stub">
  <!-- U1 placeholder -->
</div>

<style>
  .search-filter-chips { display: flex; gap: 6px; }
</style>

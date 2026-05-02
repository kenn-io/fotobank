<!-- TODO U1: replace with the real popover that surfaces date range,
     tag autocomplete, location autocomplete, and media type controls.
     For T2 this is a placeholder that exposes the filters/onChange
     contract so Search.svelte typechecks against the eventual surface. -->
<script lang="ts">
  import type { SearchFilters } from "./types";

  // The destructure declares the prop shape so Search.svelte typechecks
  // against the eventual U1 surface. Body intentionally doesn't read
  // them here — that's U1's job. ESLint allows the unused locals via
  // the eslint-config-svelte preset; svelte-check doesn't warn on
  // declared-but-unread $props() destructures.
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  let { filters, onChange }: {
    filters: SearchFilters;
    onChange?: (next: SearchFilters) => void;
  } = $props();
</script>

<div class="search-filters-popover" data-testid="search-filters-popover-stub">
  <!-- U1 placeholder -->
</div>

<style>
  .search-filters-popover { display: inline-flex; }
</style>

<!-- TODO U2: replace with the segmented control that flips between
     relevance / newest / oldest and writes the sort back through
     onChange. T2 stub exposes only the prop contract. -->
<script lang="ts">
  import type { SearchSort } from "./types";

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  let { sort, onChange }: {
    sort: SearchSort;
    onChange?: (next: SearchSort) => void;
  } = $props();
</script>

<div class="search-sort-segment" data-testid="search-sort-segment-stub">
  <!-- U2 placeholder -->
</div>

<style>
  .search-sort-segment { display: inline-flex; }
</style>

<script lang="ts">
  import VirtualGrid from "./VirtualGrid.svelte";
  import type { Month } from "../media/mediaStore.svelte";

  let { months }: { months: Month[] } = $props();
</script>

<VirtualGrid {months} onLoadMore={() => {}} targetRowHeight={200}>
  {#snippet headerAction(month)}
    <button class="ha" data-month={month.key}>{`act:${month.key}`}</button>
  {/snippet}
</VirtualGrid>

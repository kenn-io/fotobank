<script lang="ts">
  import MonthChunk from "./MonthChunk.svelte";
  // exactOptionalPropertyTypes: passing label={undefined} clashes with
  // MonthChunk's `label?: string`. Branch on presence so the undefined
  // case omits the prop entirely, matching how real callers use it.
  let { label }: { label?: string | undefined } = $props();
</script>

{#if label === undefined}
  <MonthChunk
    items={[]}
    options={{ containerWidth: 800, targetRowHeight: 200, gap: 4 }}
  >
    {#snippet headerAction()}
      <button class="test-action">ACT</button>
    {/snippet}
    {#snippet renderCell()}<span></span>{/snippet}
  </MonthChunk>
{:else}
  <MonthChunk
    items={[]}
    {label}
    options={{ containerWidth: 800, targetRowHeight: 200, gap: 4 }}
  >
    {#snippet headerAction()}
      <button class="test-action">ACT</button>
    {/snippet}
    {#snippet renderCell()}<span></span>{/snippet}
  </MonthChunk>
{/if}

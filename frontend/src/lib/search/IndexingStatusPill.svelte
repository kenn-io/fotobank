<!-- frontend/src/lib/search/IndexingStatusPill.svelte
     V1: small inline badge that surfaces embedding_completeness from
     the search response. Hidden when the library is fully indexed
     (completeness === 1) so the toolbar is uncluttered for users in
     steady state.

     Two display modes:
       - With embedded/eligible counts: "2,879 / 2,981 indexed"
       - Without: "42% indexed" derived from completeness alone.

     The fotobank backend currently returns only embedding_completeness
     (a 0..1 float, see SearchResponse.embedding_completeness in
     ./types.ts), so production callers see the percentage form. The
     embedded/eligible inputs are wired for future expansion when the
     wire shape grows a per-count breakdown. -->
<script lang="ts">
  let { completeness, embedded, eligible }: {
    completeness: number;
    embedded?: number;
    eligible?: number;
  } = $props();

  // visible hides the pill at full coverage. Strict < 1 so a backend
  // that reports e.g. 0.999 from rounding still surfaces the pill;
  // exactly 1 (the "fully indexed" sentinel) suppresses it.
  const visible = $derived(completeness < 1.0);

  // label prefers the absolute counts when both are supplied (more
  // information than a percentage); otherwise falls back to a rounded
  // percent. toLocaleString applies the user's locale separators
  // (commas in en-US, spaces in fr-FR, etc.) so the pill reads
  // naturally regardless of the deployment locale.
  const label = $derived(
    embedded != null && eligible != null
      ? `${embedded.toLocaleString()} / ${eligible.toLocaleString()} indexed`
      : `${Math.round(completeness * 100)}% indexed`,
  );
</script>

{#if visible}
  <span class="pill" data-testid="indexing-status-pill">{label}</span>
{/if}

<style>
  .pill {
    display: inline-flex;
    align-items: center;
    height: 22px;
    padding: 0 8px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--bg-elevated);
    color: var(--text-secondary);
    font-size: 12px;
    line-height: 1;
    white-space: nowrap;
  }
</style>

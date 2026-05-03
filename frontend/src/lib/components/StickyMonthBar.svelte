<!-- frontend/src/lib/components/StickyMonthBar.svelte -->
<script lang="ts">
  let { label }: { label: string } = $props();
</script>

{#if label}
  <div class="bar" aria-hidden="true">{label}</div>
{/if}

<style>
  .bar {
    position: sticky;
    top: 0;
    z-index: 5;
    padding: 4px 12px;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--ink-3);
    background: color-mix(in srgb, var(--bg) 80%, transparent);
    backdrop-filter: blur(6px);
    border-bottom: 1px solid var(--border);
  }
</style>

<!-- frontend/src/lib/components/AIStatusDot.svelte -->
<script lang="ts">
  import { aiHealthStore, deriveDot } from "../ai/health.svelte";
  import { handleInternalLinkClick } from "../router/router.svelte";
  import type { AIDotState } from "../ai/types";

  // App.svelte already calls aiHealthStore.refresh() on mount and on
  // ai.* SSE events; no per-component refresh needed.
  const dot = $derived(deriveDot(aiHealthStore.health));

  const colorByState: Record<AIDotState, string> = {
    idle: "var(--ok, #22c55e)",
    backlog: "var(--warn, #facc15)",
    failing: "var(--danger-soft, #f97316)",
    unreachable: "var(--danger, #ef4444)",
    paused: "var(--warn, #facc15)",
    hidden: "transparent",
  };

  const iconByState: Record<AIDotState, string> = {
    idle: "✓",
    backlog: "≡",
    failing: "!",
    unreachable: "⏚",
    paused: "‖",
    hidden: "",
  };
</script>

{#if dot.state !== "hidden"}
  <a
    href="/settings/ai"
    aria-label={dot.label}
    title={dot.label}
    style:--dot-color={colorByState[dot.state]}
    class="ai-dot"
    data-state={dot.state}
    onclick={(e) => handleInternalLinkClick(e, "/settings/ai")}
  >
    <span class="dot" aria-hidden="true"></span>
    <span class="icon" aria-hidden="true">{iconByState[dot.state]}</span>
  </a>
{/if}

<style>
  .ai-dot {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    text-decoration: none;
    color: inherit;
  }
  .dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--dot-color);
  }
  .icon {
    font-size: 9px;
    color: var(--text-muted);
  }
</style>

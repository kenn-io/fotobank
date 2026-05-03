<!-- frontend/src/lib/components/ToastStack.svelte -->
<script lang="ts">
  import type { ToastStore } from "../toasts/toastStore.svelte";

  let { toastStore }: { toastStore: ToastStore } = $props();
</script>

{#if toastStore.items.length > 0}
  <div class="toast-stack" role="region" aria-label="Notifications" aria-live="polite">
    {#each toastStore.items as toast (toast.id)}
      <div class="toast toast--{toast.kind ?? 'info'}" role="status">
        <div class="toast-body">
          <span class="toast-message">{toast.message}</span>
          {#if toast.details && toast.details.length > 0}
            <ul class="toast-details">
              {#each toast.details as detail}
                <li>{detail}</li>
              {/each}
            </ul>
          {/if}
        </div>
        <button
          type="button"
          class="toast-dismiss"
          aria-label="Dismiss"
          onclick={() => toastStore.dismiss(toast.id)}
        >
          ×
        </button>
      </div>
    {/each}
  </div>
{/if}

<style>
  .toast-stack {
    position: fixed;
    /* 20px: stack offset from the viewport edge; no scale stop matches
       cleanly (16 reads tight, 24 reads loose). Width clamp uses 40px
       to keep the stack 20px clear of both edges on narrow viewports. */
    bottom: 20px;
    right: 20px;
    z-index: 1000;
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    /* min(380px, 100vw - 40px) prevents the stack from extending past
       the left edge on narrow viewports (finding #13). */
    width: min(380px, calc(100vw - 40px));
  }

  .toast {
    display: flex;
    align-items: flex-start;
    /* 10px isn't a scale stop (closest is 8 or 12); kept as-is so the
       toast inner padding/gap stay tight without forcing a denser or
       looser visual. */
    gap: 10px;
    padding: 10px var(--space-5);
    border-radius: var(--radius-sm);
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    box-shadow: var(--shadow-sm);
    font-size: var(--text-base);
    color: var(--text-primary);
  }

  .toast--error {
    border-color: var(--danger);
    color: var(--danger);
  }

  .toast-body {
    flex: 1;
    min-width: 0;
  }

  .toast-message {
    display: block;
    font-weight: 500;
  }

  .toast-details {
    margin: var(--space-2) 0 0;
    padding-left: var(--space-6);
    font-size: var(--text-sm);
    color: var(--text-secondary);
  }

  .toast-details li {
    margin-bottom: var(--space-1);
  }

  .toast-dismiss {
    flex-shrink: 0;
    background: none;
    border: none;
    cursor: pointer;
    color: var(--text-muted);
    /* 18px: dismiss × keeps the existing tap-target glyph; 16px (text-lg)
       reads too small and 24px wraps. No scale stop matches. */
    font-size: 18px;
    line-height: 1;
    padding: 0 var(--space-1);
  }

  .toast-dismiss:hover {
    color: var(--text-primary);
  }
</style>

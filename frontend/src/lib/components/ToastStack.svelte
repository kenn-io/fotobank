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
    bottom: 20px;
    right: 20px;
    z-index: 1000;
    display: flex;
    flex-direction: column;
    gap: 8px;
    /* min(380px, 100vw - 40px) prevents the stack from extending past
       the left edge on narrow viewports (finding #13). */
    width: min(380px, calc(100vw - 40px));
  }

  .toast {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 12px;
    border-radius: var(--radius);
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    box-shadow: var(--shadow);
    font-size: 13px;
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
    margin: 4px 0 0;
    padding-left: 16px;
    font-size: 12px;
    color: var(--text-secondary);
  }

  .toast-details li {
    margin-bottom: 2px;
  }

  .toast-dismiss {
    flex-shrink: 0;
    background: none;
    border: none;
    cursor: pointer;
    color: var(--text-muted);
    font-size: 18px;
    line-height: 1;
    padding: 0 2px;
  }

  .toast-dismiss:hover {
    color: var(--text-primary);
  }
</style>

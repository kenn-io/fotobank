<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";

  let { message, onRetry }: {
    message: string;
    onRetry: () => Promise<void>;
  } = $props();
</script>

<div class="read-error" role="alert">
  <p>{message} Retry to load them.</p>
  <Button onclick={onRetry}>Retry</Button>
</div>

<style>
  .read-error {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-4);
    color: var(--text-secondary);
  }
  p { margin: 0; }
</style>

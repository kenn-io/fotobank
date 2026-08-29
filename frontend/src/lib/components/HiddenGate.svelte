<!-- frontend/src/lib/components/HiddenGate.svelte
     Full-route gate for hidden privacy. Renders one of:
       1. CTA — when hidden privacy is not configured
       2. Passcode form — when configured but not unlocked
       3. nothing (slot / children pass-through) — when unlocked
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import type { HiddenStore, HiddenError } from "../hidden/hiddenStore.svelte";
  import { router } from "../router/router.svelte";

  let { hiddenStore, children }: {
    hiddenStore: HiddenStore;
    children?: Snippet;
  } = $props();

  let passcode = $state("");
  let submitting = $state(false);

  async function handleSubmit(e: SubmitEvent) {
    e.preventDefault();
    if (!passcode) return;
    submitting = true;
    try {
      await hiddenStore.unlock(passcode);
      passcode = "";
    } catch {
      // Error is stored on hiddenStore.error; rendered below.
    } finally {
      submitting = false;
    }
  }

  function errorMessage(err: HiddenError): string {
    if (err.kind === "wrong_passcode") return "Passcode incorrect.";
    if (err.kind === "locked_out") {
      const min = Math.ceil(err.retryAfterSeconds / 60);
      return `Too many attempts. Try again in ${min} min.`;
    }
    if (err.kind === "invalid_input") return "Passcode must be 1–1024 bytes.";
    if (err.kind === "identity_required") return "Identity not configured.";
    return "Could not reach server.";
  }
</script>

{#if !hiddenStore.configured}
  <div class="gate-cta">
    <p>Hidden privacy isn't set up.</p>
    <p>
      Run <code>fotobank hidden setup</code> on the host to configure it.
    </p>
  </div>
{:else if !hiddenStore.unlocked}
  <div class="gate-form-wrap">
    <form class="gate-form" onsubmit={handleSubmit}>
      <h2>Hidden</h2>
      <p class="gate-hint">Enter your passcode to view hidden photos.</p>
      <!-- svelte-ignore a11y_autofocus -->
      <input
        type="password"
        bind:value={passcode}
        placeholder="Passcode"
        autofocus
        disabled={submitting}
        autocomplete="current-password"
      />
      {#if hiddenStore.error}
        <p class="gate-error">{errorMessage(hiddenStore.error)}</p>
      {/if}
      <div class="gate-actions">
        <button type="button" onclick={() => router.back("/library")} disabled={submitting}>
          Cancel
        </button>
        <button type="submit" disabled={submitting || !passcode}>
          {submitting ? "Unlocking…" : "Unlock"}
        </button>
      </div>
    </form>
  </div>
{:else if children}
  {@render children()}
{/if}

<style>
  .gate-cta {
    padding: 64px 16px;
    text-align: center;
    color: var(--text-secondary);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
  }
  .gate-cta code {
    font-family: monospace;
    background: var(--bg-inset);
    padding: 2px 6px;
    border-radius: 4px;
  }
  .gate-form-wrap {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 64px 16px;
  }
  .gate-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
    min-width: 280px;
    max-width: 360px;
    width: 100%;
  }
  .gate-form h2 { margin: 0; font-size: 18px; }
  .gate-hint { margin: 0; color: var(--text-secondary); font-size: 13px; }
  .gate-form input {
    padding: 8px 10px;
    border: 1px solid var(--border-default);
    background: var(--bg-inset);
    color: var(--text-primary);
    font-size: 14px;
    width: 100%;
    box-sizing: border-box;
  }
  .gate-error {
    margin: 0;
    color: var(--accent-red);
    font-size: 13px;
  }
  .gate-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
  }
</style>

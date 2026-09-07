<!-- frontend/src/lib/components/HiddenLockStrip.svelte
     Renders a thin bar showing time remaining until the hidden session
     expires, plus a "Lock now" button.
     Parent is responsible for mount-gating (only render when unlocked).
-->
<script lang="ts">
  import type { HiddenStore } from "../hidden/hiddenStore.svelte";

  let { hiddenStore }: { hiddenStore: HiddenStore } = $props();

  // Seconds remaining, updated every second via setInterval.
  // hiddenStore is a stable
  // constructor reference; the $effect below re-reads it reactively.
  // svelte-ignore state_referenced_locally
  let secondsLeft = $state(computeSecondsLeft(hiddenStore.expiresAt));

  function computeSecondsLeft(expiresAt: string | null): number {
    if (!expiresAt) return 0;
    const diff = Math.floor((new Date(expiresAt).getTime() - Date.now()) / 1000);
    return Math.max(0, diff);
  }

  function formatCountdown(secs: number): string {
    const m = Math.floor(secs / 60);
    const s = secs % 60;
    return `${m}:${String(s).padStart(2, "0")}`;
  }

  $effect(() => {
    // Re-read expiresAt reactively so effect re-runs if it changes.
    const expires = hiddenStore.expiresAt;
    secondsLeft = computeSecondsLeft(expires);

    let autoLocked = false;
    const intervalId = setInterval(() => {
      const remaining = computeSecondsLeft(hiddenStore.expiresAt);
      secondsLeft = remaining;
      // Auto-lock once when countdown reaches 0 (finding #7): the cookie
      // has expired so clear client state and let the gate re-appear.
      if (remaining === 0 && !autoLocked) {
        autoLocked = true;
        void hiddenStore.lock();
      }
    }, 1000);

    return () => clearInterval(intervalId);
  });
</script>

<div class="lock-strip" role="status" aria-label="Hidden session active">
  <span class="lock-strip-label">Hidden unlocked</span>
  <span class="lock-strip-countdown" aria-label="Time remaining">
    {formatCountdown(secondsLeft)}
  </span>
  <button type="button" onclick={() => hiddenStore.lock()}>
    Lock now
  </button>
</div>

<style>
  .lock-strip {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 6px 16px;
    background: var(--bg-inset);
    border-bottom: 1px solid var(--border-default);
    font-size: 13px;
    color: var(--text-secondary);
  }

  .lock-strip-label {
    font-weight: 500;
    color: var(--text-primary);
  }

  .lock-strip-countdown {
    font-variant-numeric: tabular-nums;
    color: var(--accent-amber);
    font-weight: 600;
  }

  button {
    margin-left: auto;
    padding: 4px 10px;
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: 13px;
    cursor: pointer;
  }

  button:hover {
    background: var(--bg-inset);
    border-color: var(--accent-blue);
    color: var(--accent-blue);
  }
</style>

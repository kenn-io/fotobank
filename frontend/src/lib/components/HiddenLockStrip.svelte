<!-- frontend/src/lib/components/HiddenLockStrip.svelte
     Renders a thin bar showing time remaining until the hidden session
     expires, plus a "Lock now" button.
     Parent is responsible for mount-gating (only render when unlocked).
-->
<script lang="ts">
  import type { HiddenStore } from "../hidden/hiddenStore.svelte";

  let { hiddenStore }: { hiddenStore: HiddenStore } = $props();

  // Seconds remaining, updated every second via setInterval.
  // svelte-ignore state_referenced_locally — hiddenStore is a stable
  // constructor reference; the $effect below re-reads it reactively.
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
    background: var(--surface-2);
    border-bottom: 1px solid var(--border);
    font-size: 13px;
    color: var(--ink-2);
  }

  .lock-strip-label {
    font-weight: 500;
    color: var(--ink);
  }

  .lock-strip-countdown {
    font-variant-numeric: tabular-nums;
    color: var(--warn);
    font-weight: 600;
  }

  button {
    margin-left: auto;
    padding: 4px 10px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--ink);
    font-size: 13px;
    cursor: pointer;
  }

  button:hover {
    background: var(--surface-2);
    border-color: var(--amber);
    color: var(--amber);
  }
</style>

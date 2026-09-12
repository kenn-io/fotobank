<!-- frontend/src/lib/components/IdentityChips.svelte -->
<script lang="ts">
  import type { Principal } from "../app/appConfig.svelte";

  // The HUB/USER chips at the right edge of the AppHeader. Reads
  // principal off props instead of importing AppConfigStore directly
  // so tests can supply minimal stubs and the component remains
  // pure-DOM. While `!ready` we render nothing — better than flashing
  // empty placeholders during the /me round-trip; the rest of the
  // header stays visible.
  //
  // `error` (optional) flips the HUB chip's status dot from the shared
  // success token to the shared danger token. Today AppConfigStore treats network failure as
  // "resolved disabled" so we never set this in production, but the
  // prop is wired so a future health-bound caller can flip it.
  let {
    principal,
    ready,
    error = false,
  }: {
    principal: Principal | null;
    ready: boolean;
    error?: boolean;
  } = $props();

  // The dot reads the shared semantic tokens via inline style so the test can
  // assert the active token without scraping computed styles. The
  // mockup spec pins the green-on-ok / red-on-error colors to those
  // tokens; using them here keeps the dot in lockstep with the
  // palette.
  const dotColor = $derived(error ? "var(--accent-red)" : "var(--accent-green)");
  const dotShadow = $derived(
    error
      ? "0 0 6px rgba(208,69,69,0.5)"
      : "0 0 6px rgba(74,170,106,0.5)",
  );
</script>

{#if ready && principal}
  <div class="id-chips">
    <div class="id-chip" data-testid="id-chip-hub" title="Hub (server identity)">
      <span class="id-chip-label">Hub</span>
      <span class="id-chip-value">
        <span
          class="id-chip-dot"
          style:background={dotColor}
          style:box-shadow={dotShadow}
          aria-hidden="true"
        ></span>{principal.hub}</span>
    </div>
    <div class="id-chip" data-testid="id-chip-user" title="Active user">
      <span class="id-chip-label">User</span>
      <span class="id-chip-value">{principal.handle}</span>
    </div>
  </div>
{/if}

<style>
  .id-chips {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .id-chip {
    display: inline-flex;
    align-items: stretch;
    height: 24px;
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .id-chip:hover {
    border-color: var(--border-muted);
  }
  .id-chip-label {
    display: inline-flex;
    align-items: center;
    padding: 0 8px;
    color: var(--text-muted);
    font-size: 9px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    border-right: 1px solid var(--border-default);
    background: rgba(0, 0, 0, 0.25);
  }
  .id-chip-value {
    display: inline-flex;
    align-items: center;
    padding: 0 9px;
    color: var(--text-primary);
    font-variant-numeric: tabular-nums;
  }
  .id-chip-dot {
    width: 5px;
    height: 5px;
    margin-right: 7px;
  }
  @media (max-width: 760px) {
    .id-chip-label { display: none; }
    .id-chip-value { max-width: 100px; overflow: hidden; white-space: nowrap; }
  }
</style>

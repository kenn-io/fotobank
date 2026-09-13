<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import type { ScopeListRow } from "../shares/sharesStore.svelte";
  import ShareStatePill from "./ShareStatePill.svelte";

  let {
    scopes,
    onOpen,
    onRevoke,
    onRetry,
  }: {
    scopes: ScopeListRow[];
    onOpen: (uuid: string) => void;
    onRevoke: (uuid: string) => void;
    onRetry: (uuid: string) => void;
  } = $props();

  // Coarse relative-time formatter. Settled/older shares fall back to a
  // locale date — the goal is "is this fresh or stale" at a glance, not
  // precise timestamps (the row's title attribute exposes the ISO).
  function fmtRelative(iso: string): string {
    const d = new Date(iso);
    const ms = Date.now() - d.getTime();
    const days = Math.floor(ms / (1000 * 60 * 60 * 24));
    if (days < 1) return "today";
    if (days === 1) return "yesterday";
    if (days < 30) return `${days} days ago`;
    return d.toLocaleDateString();
  }

  function granteeText(s: ScopeListRow): string {
    if (s.grantee_handle) return s.grantee_handle;
    return `${s.grantee.hub}:${s.grantee.user_id}`;
  }

  function labelText(s: ScopeListRow): string {
    if (s.label) return s.label;
    if (s.target_summary?.label) return s.target_summary.label;
    return "(unlabeled)";
  }

  // Revoking is in-flight; revoked is settled. Both should hide the
  // Revoke button so a double-click can't enqueue a duplicate request.
  function canRevoke(s: ScopeListRow): boolean {
    if (s.broker_status === "revoking" || s.broker_status === "revoked_remote") return false;
    return true;
  }

  // Retry is only meaningful when the broker grant failed. Pending rows
  // are still mid-publish — a retry there would race the in-flight job.
  function canRetry(s: ScopeListRow): boolean {
    return s.broker_status === "failed";
  }
</script>

<!-- Explicit roles preserve table semantics when the phone layout uses blocks. -->
<!-- svelte-ignore a11y_no_redundant_roles -->
<table class="shares" role="table" aria-label="Shares">
  <!-- svelte-ignore a11y_no_redundant_roles -->
  <thead role="rowgroup">
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <tr role="row">
      <th role="columnheader" scope="col">Label</th>
      <th role="columnheader" scope="col">Type</th>
      <th role="columnheader" scope="col">Grantee</th>
      <th role="columnheader" scope="col">State</th>
      <th role="columnheader" scope="col">Created</th>
      <th role="columnheader" scope="col" class="actions-col">Actions</th>
    </tr>
  </thead>
  <!-- svelte-ignore a11y_no_redundant_roles -->
  <tbody role="rowgroup">
    {#each scopes as s (s.uuid)}
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <tr role="row">
        <td role="cell" class="label-col"><button type="button" class="open-share" onclick={() => onOpen(s.uuid)}>{labelText(s)}</button></td>
        <td role="cell"><span class="mobile-label" aria-hidden="true">Type</span><span class="type">{s.target_type === "media_set" ? "Photos" : "Album"}</span></td>
        <td role="cell"><span class="mobile-label" aria-hidden="true">Grantee</span><span class="mono">{granteeText(s)}</span></td>
        <td role="cell"><span class="mobile-label" aria-hidden="true">State</span><ShareStatePill scope={s} /></td>
        <td role="cell" title={s.created_at}><span class="mobile-label" aria-hidden="true">Created</span>{fmtRelative(s.created_at)}</td>
        <td role="cell" class="actions-col">
          {#if canRetry(s)}
            <Button onclick={() => onRetry(s.uuid)}>Retry</Button>
          {/if}
          {#if canRevoke(s)}
            <Button tone="danger" onclick={() => onRevoke(s.uuid)}>Revoke</Button>
          {/if}
        </td>
      </tr>
    {/each}
  </tbody>
</table>

<style>
  table.shares { width: 100%; border-collapse: collapse; }
  th, td { padding: 8px 12px; text-align: left; border-bottom: 1px solid var(--border-default); }
  th { background: var(--bg-inset); color: var(--text-muted); font-weight: 500; font-size: 12px; }
  .mobile-label { display: none; }
  .open-share { border: 0; padding: 8px 0; background: transparent; color: var(--accent-blue); font: inherit; text-align: left; text-decoration: underline; text-underline-offset: 3px; cursor: pointer; }
  button:focus-visible { outline: 2px solid var(--accent-blue); outline-offset: 3px; }
  .actions-col { width: 1%; white-space: nowrap; }
  .type { color: var(--text-secondary); font-size: 12px; }
  .mono { font-family: monospace; font-size: 13px; }
  @media (max-width: 760px) {
    /* Keep explicit table roles when CSS changes its visual layout. */
    table.shares, tbody, tr { display: block; }
    thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
    tr { padding: 12px 16px; border-bottom: 1px solid var(--border-default); }
    td { display: flex; align-items: baseline; gap: 12px; padding: 4px 0; border: 0; overflow-wrap: anywhere; }
    .mobile-label { display: inline-block; flex: 0 0 64px; color: var(--text-secondary); }
    .label-col { display: block; }
    .open-share { min-height: 44px; font-size: var(--font-size-lg); }
    .actions-col { width: auto; white-space: normal; margin-top: 8px; }
    .actions-col :global(button) { min-height: 44px; }
  }
</style>

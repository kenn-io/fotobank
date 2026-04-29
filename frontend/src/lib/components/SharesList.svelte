<script lang="ts">
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

<table class="shares">
  <thead>
    <tr>
      <th>Label</th>
      <th>Type</th>
      <th>Grantee</th>
      <th>State</th>
      <th>Created</th>
      <th class="actions-col">Actions</th>
    </tr>
  </thead>
  <tbody>
    {#each scopes as s (s.uuid)}
      <!-- Row click opens detail. The actions cell stops propagation so
           clicking Revoke/Retry doesn't also fire the row open. -->
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <tr onclick={() => onOpen(s.uuid)}>
        <td>{labelText(s)}</td>
        <td><span class="type">{s.target_type === "media_set" ? "Photos" : "Album"}</span></td>
        <td class="mono">{granteeText(s)}</td>
        <td><ShareStatePill scope={s} /></td>
        <td title={s.created_at}>{fmtRelative(s.created_at)}</td>
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <td class="actions-col" onclick={(e) => e.stopPropagation()}>
          {#if canRetry(s)}
            <button type="button" onclick={() => onRetry(s.uuid)}>Retry</button>
          {/if}
          {#if canRevoke(s)}
            <button type="button" class="danger" onclick={() => onRevoke(s.uuid)}>Revoke</button>
          {/if}
        </td>
      </tr>
    {/each}
  </tbody>
</table>

<style>
  table.shares { width: 100%; border-collapse: collapse; }
  th, td { padding: 8px 12px; text-align: left; border-bottom: 1px solid var(--border); }
  th { background: var(--bg-elevated); color: var(--text-muted); font-weight: 500; font-size: 12px; }
  tbody tr { cursor: pointer; }
  tbody tr:hover { background: var(--bg-elevated); }
  .actions-col { width: 1%; white-space: nowrap; }
  .type { color: var(--text-muted); font-size: 12px; }
  .mono { font-family: monospace; font-size: 13px; }
  .danger { color: var(--danger); border-color: var(--danger); }
</style>

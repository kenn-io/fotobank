<script lang="ts">
  import type { ScopeListRow } from "../shares/sharesStore.svelte";

  let { scope }: { scope: ScopeListRow } = $props();

  type Visual = { label: string; cls: string; icon: string; aria: string };

  // $derived.by(...) — multi-statement derived returning an object.
  // $derived(() => ...) would yield a function-typed value (see
  // ShareModal.svelte:35, AlbumDetail.svelte:41 precedent).
  const visual: Visual = $derived.by((): Visual => {
    // Expired override: a future expires_at shouldn't override broker
    // status, but a past one should — except when broker_status is
    // already revoked_remote (the share is settled regardless).
    if (scope.expires_at) {
      const expired = new Date(scope.expires_at) < new Date();
      if (expired && scope.broker_status !== "revoked_remote") {
        return { label: "Expired", cls: "muted", icon: "⌛", aria: "Expired" };
      }
    }
    switch (scope.broker_status) {
      case "pending":
        return { label: "Pending", cls: "warn", icon: "⧗", aria: "Pending publish" };
      case "active":
        return { label: "Active", cls: "ok", icon: "✓", aria: "Active" };
      case "failed":
        return { label: "Failed", cls: "danger", icon: "!", aria: "Publish failed" };
      case "revoking":
        return { label: "Revoking…", cls: "orange", icon: "↻", aria: "Revoking" };
      case "revoked_remote":
      default:
        return { label: "Revoked", cls: "muted", icon: "—", aria: "Revoked" };
    }
  });
</script>

<span class="pill {visual.cls}" aria-label={visual.aria}>
  <span class="icon" aria-hidden="true">{visual.icon}</span>
  <span class="label">{visual.label}</span>
</span>

<style>
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 8px;
    border-radius: 12px;
    font-size: 12px;
    border: 1px solid currentColor;
  }
  .pill.warn { color: var(--warn); }
  .pill.ok { color: var(--ok, #16a34a); }
  .pill.danger { color: var(--danger); }
  .pill.orange { color: var(--orange, #d97706); }
  .pill.muted { color: var(--text-muted); }
</style>

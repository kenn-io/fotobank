<!-- frontend/src/routes/SettingsAI.svelte -->
<script lang="ts">
  import { aiHealthStore } from "../lib/ai/health.svelte";
  import { AIInspectionStore } from "../lib/ai/inspectionStore.svelte";
  import { api } from "../lib/api/client";
  import {
    acknowledgeHiddenProcessing,
    backfillAI,
    listAIFailures,
    retryFailedAI,
  } from "../lib/ai/client";
  import type { AIFailureRow, AITask } from "../lib/ai/types";
  import type { AppConfigStore } from "../lib/app/appConfig.svelte";

  // Tests inject a stubbed inspectionStore so the toggle can be driven
  // without an HTTP roundtrip; production callers omit the prop and
  // get a fresh store backed by the same-origin API client.
  let { inspectionStore = new AIInspectionStore(api), appConfig }: {
    inspectionStore?: AIInspectionStore;
    appConfig?: AppConfigStore;
  } = $props();

  let tagFailures = $state<AIFailureRow[]>([]);
  let captionFailures = $state<AIFailureRow[]>([]);
  let busy = $state<string | null>(null);

  void refreshAll();
  // Hydrate the toggle's persisted value on mount. Failures are silent
  // — the default (false) is correct on a fresh account, and a load
  // failure on an existing account just leaves the toggle off until
  // the user clicks it (which writes a fresh value).
  // svelte-ignore state_referenced_locally
  void inspectionStore.load().catch(() => {});

  async function refreshAll(): Promise<void> {
    await aiHealthStore.refresh();
    const [tags, captions] = await Promise.all([
      listAIFailures("tag", 5).catch(() => []),
      listAIFailures("caption", 5).catch(() => []),
    ]);
    tagFailures = tags;
    captionFailures = captions;
  }

  async function toggleInspection(e: Event): Promise<void> {
    const next = (e.currentTarget as HTMLInputElement).checked;
    busy = "inspection";
    try {
      await inspectionStore.set(next);
    } finally {
      busy = null;
    }
  }

  async function ack(): Promise<void> {
    busy = "ack";
    try {
      await acknowledgeHiddenProcessing();
      await refreshAll();
    } finally {
      busy = null;
    }
  }

  async function backfill(task: AITask): Promise<void> {
    busy = `backfill-${task}`;
    try {
      await backfillAI(task);
      await refreshAll();
    } finally {
      busy = null;
    }
  }

  async function retryAll(task: AITask): Promise<void> {
    busy = `retry-${task}`;
    try {
      await retryFailedAI(task);
      await refreshAll();
    } finally {
      busy = null;
    }
  }
</script>

<section class="ai-panel">
  <header class="panel-header">
    <h2>AI</h2>
    {#if appConfig?.adminSettingsEnabled}
      <a class="admin-link" href="/admin/settings/ai">Configure AI…</a>
    {:else}
      <span class="config-locked" title="Toggle in config.toml">Enabled via config.toml</span>
    {/if}
  </header>

  {#if !aiHealthStore.health}
    <p class="muted">Loading…</p>
  {:else if aiHealthStore.health.paused_reason === "config_disabled"}
    <p class="muted">
      AI is disabled in config.toml.
      {#if appConfig?.adminSettingsEnabled}
        <a href="/admin/settings/ai">Configure AI…</a>
      {/if}
    </p>
  {:else if aiHealthStore.health.paused_reason === "acknowledgement_required"}
    <div class="ack-modal" role="dialog" aria-labelledby="ack-title">
      <h3 id="ack-title">Before AI starts processing your library</h3>
      <p>
        Hidden photos will be tagged and captioned alongside everything else.
        Outputs stay owner-only and are only visible inside the unlocked Hidden context,
        but the image bytes are sent to the configured AI endpoint.
      </p>
      <button type="button" onclick={ack} disabled={busy === "ack"}>
        {busy === "ack" ? "Acknowledging…" : "Acknowledge and start workers"}
      </button>
    </div>
  {:else}
    {@const tag = aiHealthStore.health.tag}
    {@const cap = aiHealthStore.health.caption}
    <div class="banner" role="note">
      <strong>Hidden photos are processed.</strong>
      Outputs stay owner-only and only show inside the unlocked Hidden context, but the image bytes are sent to the configured AI endpoint.
    </div>

    <div class="vision">
      <span class="dot" data-reachable={aiHealthStore.health.vision.reachable} aria-hidden="true"></span>
      {aiHealthStore.health.vision.reachable
        ? "Reachable"
        : aiHealthStore.health.vision.last_error || "Unreachable"}
    </div>

    <article class="task-card">
      <header class="task-header">
        <strong>Tag</strong>
        <span class="meta">{tag.active_fingerprint}</span>
        <button type="button" onclick={() => backfill("tag")} disabled={busy === "backfill-tag"}>
          {busy === "backfill-tag" ? "Enqueuing…" : "Backfill all"}
        </button>
      </header>
      <ul class="counters">
        <li>Done <strong>{tag.done}</strong></li>
        <li>Pending <strong>{tag.pending}</strong></li>
        <li>Working <strong>{tag.working}</strong></li>
        <li>Skipped <strong>{tag.skipped}</strong></li>
        <li>
          Failed <strong>{tag.failed_active}</strong>
          {#if tag.failed_active > 0}
            <button type="button" class="link" onclick={() => retryAll("tag")} disabled={busy === "retry-tag"}>
              retry all
            </button>
          {/if}
        </li>
      </ul>
    </article>

    <article class="task-card">
      <header class="task-header">
        <strong>Caption</strong>
        <span class="meta">{cap.active_fingerprint}</span>
        <button type="button" onclick={() => backfill("caption")} disabled={busy === "backfill-caption"}>
          {busy === "backfill-caption" ? "Enqueuing…" : "Backfill all"}
        </button>
      </header>
      <ul class="counters">
        <li>Done <strong>{cap.done}</strong></li>
        <li>Pending <strong>{cap.pending}</strong></li>
        <li>Working <strong>{cap.working}</strong></li>
        <li>Skipped <strong>{cap.skipped}</strong></li>
        <li>
          Failed <strong>{cap.failed_active}</strong>
          {#if cap.failed_active > 0}
            <button type="button" class="link" onclick={() => retryAll("caption")} disabled={busy === "retry-caption"}>
              retry all
            </button>
          {/if}
        </li>
      </ul>
    </article>

    {#if tagFailures.length > 0 || captionFailures.length > 0}
      <section class="failures">
        <h4>Recent failures</h4>
        <ul>
          {#each tagFailures as f (f.media_id + ":tag")}
            <li><span class="badge">tag</span> {f.media_id} — {f.last_error_kind}: {f.last_error}</li>
          {/each}
          {#each captionFailures as f (f.media_id + ":caption")}
            <li><span class="badge">caption</span> {f.media_id} — {f.last_error_kind}: {f.last_error}</li>
          {/each}
        </ul>
      </section>
    {/if}
  {/if}

  <!-- AI Inspection toggle. Sits outside the health-gated branches so a
       config-disabled or unacknowledged deployment can still toggle the
       diagnostics surface ahead of an admin flip. The toggle's only
       effect is on the search page: when on, every search request adds
       explain=true and per-result score breakdowns appear in the grid
       and the lightbox metadata row. -->
  <section class="inspection">
    <h4>AI Inspection</h4>
    <p class="muted">
      Show search relevance score breakdowns (RRF, BM25, vector) on each
      result. Useful for tuning queries and understanding why a photo
      ranked where it did.
    </p>
    <label class="toggle">
      <input
        type="checkbox"
        checked={inspectionStore.enabled}
        onchange={toggleInspection}
        disabled={busy === "inspection"}
        data-testid="ai-inspection-toggle"
      />
      <span>Enable AI Inspection</span>
    </label>
  </section>
</section>

<style>
  .ai-panel { padding: 16px; max-width: 720px; }
  .panel-header { display: flex; justify-content: space-between; align-items: baseline; }
  .admin-link { font-size: 12px; color: var(--accent); text-decoration: none; }
  .config-locked { font-size: 11px; color: var(--ink-3); }
  .banner { padding: 10px; border-radius: 6px; background: rgba(250, 204, 21, 0.1); border: 1px solid rgba(250, 204, 21, 0.4); margin: 12px 0; font-size: 11px; }
  .ack-modal { padding: 14px; border-radius: 6px; background: var(--surface); border: 1px solid var(--border); margin: 12px 0; }
  .ack-modal h3 { margin: 0 0 8px 0; font-size: 13px; }
  .vision { display: flex; align-items: center; gap: 6px; font-size: 11px; margin: 6px 0 12px; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--danger, #ef4444); }
  .dot[data-reachable="true"] { background: var(--ok, #22c55e); }
  .task-card { background: var(--surface); border-radius: 6px; padding: 10px 12px; margin-bottom: 10px; }
  .task-header { display: flex; gap: 8px; align-items: center; }
  .meta { font-size: 10px; color: var(--ink-3); flex: 1; }
  .counters { list-style: none; padding: 0; margin: 8px 0 0; display: grid; grid-template-columns: repeat(5, 1fr); gap: 8px; font-size: 10px; }
  .counters li { color: var(--ink-3); }
  .counters strong { color: var(--ink); display: block; font-size: 14px; }
  button.link { font-size: 9px; background: none; border: none; color: var(--ink-2); text-decoration: underline; padding: 0; cursor: pointer; }
  .failures ul { list-style: none; padding: 0; font-size: 10px; line-height: 1.6; }
  .badge { display: inline-block; padding: 1px 5px; border-radius: 3px; font-size: 9px; background: var(--surface-2); color: var(--ink-3); margin-right: 6px; }
  .inspection { margin-top: 16px; padding: 10px 12px; background: var(--surface); border-radius: 6px; }
  .inspection h4 { margin: 0 0 4px 0; font-size: 12px; }
  .inspection .muted { font-size: 10px; color: var(--ink-3); margin: 0 0 8px 0; line-height: 1.5; }
  .toggle { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; cursor: pointer; }
</style>

<!-- frontend/src/routes/SettingsAI.svelte -->
<script lang="ts">
  import { aiHealthStore } from "../lib/ai/health.svelte";
  import {
    acknowledgeHiddenProcessing,
    backfillAI,
    listAIFailures,
    retryFailedAI,
  } from "../lib/ai/client";
  import type { AIFailureRow, AITask } from "../lib/ai/types";

  let tagFailures = $state<AIFailureRow[]>([]);
  let captionFailures = $state<AIFailureRow[]>([]);
  let busy = $state<string | null>(null);

  void refreshAll();

  async function refreshAll(): Promise<void> {
    await aiHealthStore.refresh();
    const [tags, captions] = await Promise.all([
      listAIFailures("tag", 5).catch(() => []),
      listAIFailures("caption", 5).catch(() => []),
    ]);
    tagFailures = tags;
    captionFailures = captions;
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
    <span class="config-locked" title="Toggle in config.toml">Enabled via config.toml</span>
  </header>

  {#if !aiHealthStore.health}
    <p class="muted">Loading…</p>
  {:else if aiHealthStore.health.paused_reason === "config_disabled"}
    <p class="muted">AI is disabled in config.toml.</p>
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
</section>

<style>
  .ai-panel { padding: 16px; max-width: 720px; }
  .panel-header { display: flex; justify-content: space-between; align-items: baseline; }
  .config-locked { font-size: 11px; color: var(--text-muted); }
  .banner { padding: 10px; border-radius: 6px; background: rgba(250, 204, 21, 0.1); border: 1px solid rgba(250, 204, 21, 0.4); margin: 12px 0; font-size: 11px; }
  .ack-modal { padding: 14px; border-radius: 6px; background: var(--bg-surface); border: 1px solid var(--border); margin: 12px 0; }
  .ack-modal h3 { margin: 0 0 8px 0; font-size: 13px; }
  .vision { display: flex; align-items: center; gap: 6px; font-size: 11px; margin: 6px 0 12px; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--danger, #ef4444); }
  .dot[data-reachable="true"] { background: var(--ok, #22c55e); }
  .task-card { background: var(--bg-surface); border-radius: 6px; padding: 10px 12px; margin-bottom: 10px; }
  .task-header { display: flex; gap: 8px; align-items: center; }
  .meta { font-size: 10px; color: var(--text-muted); flex: 1; }
  .counters { list-style: none; padding: 0; margin: 8px 0 0; display: grid; grid-template-columns: repeat(5, 1fr); gap: 8px; font-size: 10px; }
  .counters li { color: var(--text-muted); }
  .counters strong { color: var(--text-primary); display: block; font-size: 14px; }
  button.link { font-size: 9px; background: none; border: none; color: var(--text-secondary); text-decoration: underline; padding: 0; cursor: pointer; }
  .failures ul { list-style: none; padding: 0; font-size: 10px; line-height: 1.6; }
  .badge { display: inline-block; padding: 1px 5px; border-radius: 3px; font-size: 9px; background: var(--bg-elevated); color: var(--text-muted); margin-right: 6px; }
</style>

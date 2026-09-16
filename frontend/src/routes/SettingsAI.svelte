<!-- frontend/src/routes/SettingsAI.svelte -->
<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
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
  let healthError = $state(false);
  let actionError = $state("");
  let notice = $state("");
  let failuresError = $state(false);

  void refreshAll();
  // Hydrate the toggle's persisted value on mount. Failures are silent
  // — the default (false) is correct on a fresh account, and a load
  // failure on an existing account just leaves the toggle off until
  // the user clicks it (which writes a fresh value).
  // svelte-ignore state_referenced_locally
  void inspectionStore.load().catch(() => {});

  async function refreshAll(): Promise<void> {
    healthError = false;
    try {
      await aiHealthStore.refresh();
    } catch {
      healthError = true;
      return;
    }
    failuresError = false;
    try {
      [tagFailures, captionFailures] = await Promise.all([
        listAIFailures("tag", 5),
        listAIFailures("caption", 5),
      ]);
    } catch {
      failuresError = true;
    }
  }

  async function runAction(key: string, work: () => Promise<string>, failure: string): Promise<void> {
    if (busy) return;
    busy = key;
    actionError = "";
    notice = "";
    try {
      notice = await work();
    } catch {
      actionError = failure;
    } finally {
      busy = null;
    }
  }

  async function toggleInspection(e: Event): Promise<void> {
    const input = e.currentTarget as HTMLInputElement;
    const next = input.checked;
    await runAction("inspection", async () => {
      await inspectionStore.set(next);
      return `AI Inspection ${next ? "enabled" : "disabled"}.`;
    }, "Couldn’t save AI Inspection. Try again.");
    input.checked = inspectionStore.enabled;
  }

  async function ack(): Promise<void> {
    await runAction("ack", async () => {
      await acknowledgeHiddenProcessing();
      await refreshAll();
      return "Acknowledgement saved.";
    }, "Couldn’t save your acknowledgement. Try again.");
  }

  async function backfill(task: AITask): Promise<void> {
    await runAction(`backfill-${task}`, async () => {
      const result = await backfillAI(task);
      await refreshAll();
      return `Queued ${result.enqueued} photos for ${task === "tag" ? "tagging" : "captioning"}.`;
    }, "Couldn’t queue photos. Try again.");
  }

  async function retryAll(task: AITask): Promise<void> {
    await runAction(`retry-${task}`, async () => {
      const result = await retryFailedAI(task);
      await refreshAll();
      return `Queued ${result.enqueued} photos to retry ${task === "tag" ? "tagging" : "captioning"}.`;
    }, "Couldn’t queue failed photos. Try again.");
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
  <p class="intro">Manage optional tagging and captions. These actions send photos to your configured AI provider.</p>
  {#if actionError}<p class="feedback" role="alert">{actionError}</p>{/if}
  {#if notice}<p class="feedback" role="status">{notice}</p>{/if}

  {#if healthError || aiHealthStore.unavailable}
    <div role="alert">
      <p>Couldn’t load AI status.</p>
      <Button onclick={refreshAll}>Retry</Button>
    </div>
  {:else if !aiHealthStore.health}
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
      <Button onclick={ack} disabled={busy !== null}>
        {busy === "ack" ? "Acknowledging…" : "Acknowledge and start workers"}
      </Button>
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
        <Button onclick={() => backfill("tag")} disabled={busy !== null}>
          {busy === "backfill-tag" ? "Enqueuing…" : "Backfill all"}
        </Button>
      </header>
      <details class="model-details"><summary>Model details</summary><code>{tag.active_fingerprint}</code></details>
      <ul class="counters">
        <li>Done <strong>{tag.done}</strong></li>
        <li>Pending <strong>{tag.pending}</strong></li>
        <li>Working <strong>{tag.working}</strong></li>
        <li>Skipped <strong>{tag.skipped}</strong></li>
        <li>
          Failed <strong>{tag.failed_active}</strong>
          {#if tag.failed_active > 0}
            <Button onclick={() => retryAll("tag")} disabled={busy !== null}>Retry failed</Button>
          {/if}
        </li>
      </ul>
    </article>

    <article class="task-card">
      <header class="task-header">
        <strong>Caption</strong>
        <Button onclick={() => backfill("caption")} disabled={busy !== null}>
          {busy === "backfill-caption" ? "Enqueuing…" : "Backfill all"}
        </Button>
      </header>
      <details class="model-details"><summary>Model details</summary><code>{cap.active_fingerprint}</code></details>
      <ul class="counters">
        <li>Done <strong>{cap.done}</strong></li>
        <li>Pending <strong>{cap.pending}</strong></li>
        <li>Working <strong>{cap.working}</strong></li>
        <li>Skipped <strong>{cap.skipped}</strong></li>
        <li>
          Failed <strong>{cap.failed_active}</strong>
          {#if cap.failed_active > 0}
            <Button onclick={() => retryAll("caption")} disabled={busy !== null}>Retry failed</Button>
          {/if}
        </li>
      </ul>
    </article>

    {#if failuresError}
      <div role="alert">
        <p>Couldn’t load recent failures.</p>
        <Button onclick={refreshAll}>Retry</Button>
      </div>
    {:else if tagFailures.length > 0 || captionFailures.length > 0}
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
        disabled={busy !== null}
        data-testid="ai-inspection-toggle"
      />
      <span>Enable AI Inspection</span>
    </label>
  </section>
</section>

<style>
  .ai-panel { padding: 24px; max-width: 800px; font-size: 14px; line-height: 1.6; }
  .panel-header { display: flex; flex-wrap: wrap; gap: 12px; justify-content: space-between; align-items: baseline; }
  .panel-header h2 { margin: 0; }
  .admin-link { color: var(--accent-blue); }
  .config-locked, .intro, .muted { color: var(--text-muted); }
  .intro { margin: 12px 0 24px; }
  .feedback { padding: 12px 16px; background: var(--bg-surface); border: 1px solid var(--border-default); }
  .banner { padding: 16px; background: var(--bg-surface); border: 1px solid var(--border-default); margin: 16px 0; }
  .ack-modal { padding: 16px; background: var(--bg-surface); border: 1px solid var(--border-default); margin: 16px 0; }
  .ack-modal h3 { margin: 0 0 8px; font-size: 16px; }
  .vision { display: flex; align-items: center; gap: 8px; margin: 16px 0; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent-red); }
  .dot[data-reachable="true"] { background: var(--accent-green); }
  .task-card { padding: 20px 0; border-top: 1px solid var(--border-default); }
  .task-header { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 12px; align-items: center; }
  .model-details { margin: 12px 0; color: var(--text-muted); font-size: 12px; }
  .model-details summary { cursor: pointer; }
  .model-details code { display: block; overflow-wrap: anywhere; }
  .counters { list-style: none; padding: 0; margin: 16px 0 0; display: grid; grid-template-columns: repeat(auto-fit, minmax(110px, 1fr)); gap: 16px; }
  .counters li { color: var(--text-muted); }
  .counters strong { color: var(--text-primary); display: block; font-size: 16px; font-variant-numeric: tabular-nums; }
  .failures ul { list-style: none; padding: 0; overflow-wrap: anywhere; }
  .failures li { margin-bottom: 12px; }
  .badge { color: var(--text-muted); margin-right: 8px; }
  .inspection { margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--border-default); }
  .inspection h4 { margin: 0 0 8px; font-size: 16px; }
  .inspection .muted { margin: 0 0 12px; }
  .toggle { display: inline-flex; align-items: center; gap: 8px; min-height: 44px; cursor: pointer; }
  @media (max-width: 760px) {
    .ai-panel { padding: 16px; }
    .ai-panel :global(button), .admin-link, .model-details summary { min-height: 44px; }
    .admin-link { display: inline-flex; align-items: center; }
    .model-details summary { align-content: center; }
  }
</style>

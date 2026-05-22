// Typed fetch wrappers for /api/v1/ai/*. The AI surface isn't included
// in the generated openapi schema (the OpenAPI dumper passes Deps{} so
// AI routes register only at runtime), so these wrappers carry their
// own types via ./types.ts.

import type { AIFailureRow, AIHealth, AITask } from "./types";

const baseHeaders = { "Content-Type": "application/json" };

export async function getAIHealth(): Promise<AIHealth> {
  const r = await fetch("/api/v1/ai/health");
  if (!r.ok) throw new Error(`/ai/health ${r.status}`);
  return (await r.json()) as AIHealth;
}

export async function listAIFailures(task: AITask, limit = 5): Promise<AIFailureRow[]> {
  const r = await fetch(`/api/v1/ai/failures?task=${task}&limit=${limit}`);
  if (!r.ok) throw new Error(`/ai/failures ${r.status}`);
  const body = (await r.json()) as { rows: AIFailureRow[] | null };
  return body.rows ?? [];
}

export async function backfillAI(
  task: AITask,
  opts?: { force?: boolean },
): Promise<{ enqueued: number }> {
  const r = await fetch("/api/v1/ai/backfill", {
    method: "POST",
    headers: baseHeaders,
    body: JSON.stringify({ task, force: opts?.force ?? false, scope: "all" }),
  });
  if (!r.ok) throw new Error(`/ai/backfill ${r.status}`);
  return (await r.json()) as { enqueued: number };
}

export async function retryFailedAI(task: AITask): Promise<{ enqueued: number }> {
  const r = await fetch("/api/v1/ai/retry-failed", {
    method: "POST",
    headers: baseHeaders,
    body: JSON.stringify({ task }),
  });
  if (!r.ok) throw new Error(`/ai/retry-failed ${r.status}`);
  return (await r.json()) as { enqueued: number };
}

export async function retryPhotoAI(mediaId: string, task: AITask): Promise<void> {
  const r = await fetch("/api/v1/ai/retry-photo", {
    method: "POST",
    headers: baseHeaders,
    body: JSON.stringify({ media_id: mediaId, task }),
  });
  if (!r.ok) throw new Error(`/ai/retry-photo ${r.status}`);
}

export async function acknowledgeHiddenProcessing(): Promise<void> {
  const r = await fetch("/api/v1/ai/acknowledge", {
    method: "POST",
    headers: baseHeaders,
    body: JSON.stringify({ kind: "hidden_processing" }),
  });
  if (!r.ok) throw new Error(`/ai/acknowledge ${r.status}`);
}

// AIMediaView is the per-photo lightbox bundle. Mirrors the
// aiservice.MediaView wire shape; all fields are optional because the
// backend omits them when there's no row.
export interface AIMediaView {
  tags?: Array<{ key: string; label: string; rank: number }>;
  caption?: {
    text: string;
    model_id: string;
    prompt_version: string;
    generated_at: string;
  };
  skipped?: { reason: string };
  tag_failure?: { kind: string; message: string };
  caption_failure?: { kind: string; message: string };
}

export async function getMediaAIView(mediaId: string): Promise<AIMediaView> {
  const r = await fetch(`/api/v1/media/${encodeURIComponent(mediaId)}/ai`);
  if (!r.ok) throw new Error(`/media/${mediaId}/ai ${r.status}`);
  return (await r.json()) as AIMediaView;
}

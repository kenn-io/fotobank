import * as client from "../api/generated/client";
import type { APIResult } from "../api/transport";
import type { AIFailureRow, AIHealth, AITask } from "./types";
export type { MediaView as AIMediaView } from "../api/generated/models";
import type { MediaView } from "../api/generated/models";

function result<T>(response: APIResult<T>): T {
  if (response.error) throw new Error(`${response.response.url} ${response.response.status}`);
  return response.data as T;
}

export async function getAIHealth(): Promise<AIHealth> {
  return result(await client.aiHealth()) as AIHealth;
}
export async function listAIFailures(task: AITask, limit = 5): Promise<AIFailureRow[]> {
  return (result(await client.aiFailures({ task, limit })).rows ?? []) as AIFailureRow[];
}
export async function backfillAI(task: AITask, opts?: { force?: boolean }): Promise<{ enqueued: number }> {
  return result(await client.aiBackfill({ task, force: opts?.force ?? false, scope: "all" }));
}
export async function retryFailedAI(task: AITask): Promise<{ enqueued: number }> {
  return result(await client.aiRetryFailed({ task }));
}
export async function retryPhotoAI(mediaId: string, task: AITask): Promise<void> {
  result(await client.aiRetryPhoto({ media_id: mediaId, task }));
}
export async function acknowledgeHiddenProcessing(): Promise<void> {
  result(await client.aiAcknowledge({ kind: "hidden_processing" }));
}
export async function getMediaAIView(mediaId: string): Promise<MediaView> {
  return result(await client.aiMediaView(mediaId));
}

// Reactive store for AI health state.
//
// The store exposes the AIHealth snapshot returned by /api/v1/ai/health
// and a refresh() method that the SSE wiring in App.svelte calls when
// ai.tag.completed / ai.caption.completed / ai.health.changed events
// arrive. An in-flight guard de-dupes concurrent refreshes so a burst
// of events triggers exactly one fetch.
//
// deriveDot() collapses the health snapshot into the AIDotInfo state
// machine that drives the global status dot in the shell.

import { getAIHealth } from "./client";
import type { AIDotInfo, AIHealth } from "./types";

export class AIHealthStore {
  health = $state<AIHealth | null>(null);
  private inflight: Promise<void> | null = null;

  async refresh(): Promise<void> {
    if (this.inflight) return this.inflight;
    this.inflight = (async () => {
      try {
        this.health = await getAIHealth();
      } finally {
        this.inflight = null;
      }
    })();
    return this.inflight;
  }
}

const FAILING_THRESHOLD = 10;
const BACKLOG_THRESHOLD = 1000;

export function deriveDot(h: AIHealth | null): AIDotInfo {
  if (!h) return { state: "hidden", label: "" };
  if (h.paused_reason === "config_disabled") return { state: "hidden", label: "" };
  if (h.paused_reason === "acknowledgement_required") {
    return { state: "paused", label: "AI paused — acknowledgement required" };
  }
  if (!h.vision.reachable) return { state: "unreachable", label: "AI endpoint unreachable" };
  const failed = h.tag.failed_active + h.caption.failed_active;
  if (failed > FAILING_THRESHOLD) return { state: "failing", label: "AI failures need attention" };
  const pending = h.tag.pending + h.caption.pending;
  if (pending > BACKLOG_THRESHOLD) return { state: "backlog", label: "AI backlog" };
  return { state: "idle", label: "AI healthy" };
}

// Singleton — App.svelte and components share one instance.
export const aiHealthStore = new AIHealthStore();

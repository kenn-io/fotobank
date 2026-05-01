// Reactive store for AI health state.
//
// The store exposes the AIHealth snapshot returned by /api/v1/ai/health
// and a refresh() method that the SSE wiring in App.svelte calls when
// ai.tag.completed / ai.caption.completed / ai.health.changed events
// arrive. The in-flight guard collapses concurrent calls into one fetch
// AND records that another refresh was requested while one was in
// flight, so an event that lands during a slow request still triggers a
// follow-up fetch. Without that re-fire, an `ai.health.changed` event
// that arrives during the initial mount fetch would be lost and the
// snapshot could remain stale until the next event.
//
// deriveDot() collapses the health snapshot into the AIDotInfo state
// machine that drives the global status dot in the shell.

import { getAIHealth } from "./client";
import type { AIDotInfo, AIHealth } from "./types";

export class AIHealthStore {
  health = $state<AIHealth | null>(null);
  private inflight: Promise<void> | null = null;
  private pending = false;

  async refresh(): Promise<void> {
    if (this.inflight) {
      // A refresh is already running — record that the snapshot is now
      // stale, then return the in-flight promise. The current run will
      // notice the flag and chain another fetch when it settles.
      this.pending = true;
      return this.inflight;
    }
    this.inflight = (async () => {
      try {
        do {
          this.pending = false;
          this.health = await getAIHealth();
        } while (this.pending);
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

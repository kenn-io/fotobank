export type AITask = "tag" | "caption";

// Reasons the AI worker is paused. Empty string means "not paused".
// "config_disabled" — [ai].enabled=false in config.
// "acknowledgement_required" — owner has hidden media but hasn't
// acknowledged the hidden-processing notice yet.
export type AIPausedReason = "" | "config_disabled" | "acknowledgement_required";

export interface AIVisionPart {
  reachable: boolean;
  last_check_at: string;
  last_error?: string;
}

export interface AITaskPart {
  active_fingerprint: string;
  pending: number;
  working: number;
  blocked: number;
  failed_active: number;
  skipped: number;
  done: number;
  throughput_per_min: number;
  last_completed_at?: string;
}

export interface AIHealth {
  enabled: boolean;
  paused_reason: AIPausedReason;
  vision: AIVisionPart;
  tag: AITaskPart;
  caption: AITaskPart;
}

export interface AIFailureRow {
  media_id: string;
  last_error: string;
  last_error_kind: string;
  attempt_count: number;
  failed_at: string;
}

// AIDotState drives the global AIStatusDot indicator in the shell:
//   idle        — everything's done, nothing to surface
//   backlog     — pending+working > 0
//   failing     — failed_active > 0
//   unreachable — vision.reachable === false
//   paused      — paused_reason !== ""
//   hidden      — dot is suppressed (e.g. AI surface disabled at build time)
export type AIDotState = "idle" | "backlog" | "failing" | "unreachable" | "paused" | "hidden";

export interface AIDotInfo {
  state: AIDotState;
  // aria-label text for screen readers; mirrors the visible tooltip.
  label: string;
}

package ai

import (
	"context"
	"time"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/owners"
)

// Probe abstracts the gateway HealthCheck. Tests pass a stub.
type Probe interface {
	Probe(ctx context.Context) error
}

// HealthInput is the per-call info that doesn't live on the service:
// whether [ai].enabled is true and a probe handle.
type HealthInput struct {
	Enabled bool
	Probe   Probe
}

// Health is the aggregate health response.
type Health struct {
	Enabled      bool       `json:"enabled"`
	PausedReason string     `json:"paused_reason"`
	Vision       VisionPart `json:"vision"`
	Tag          TaskPart   `json:"tag"`
	Caption      TaskPart   `json:"caption"`
}

// VisionPart is the gateway reachability sub-block.
type VisionPart struct {
	Reachable   bool      `json:"reachable"`
	LastCheckAt time.Time `json:"last_check_at"`
	LastError   string    `json:"last_error,omitempty"`
}

// TaskPart is the per-task sub-block.
type TaskPart struct {
	ActiveFingerprint string     `json:"active_fingerprint"`
	Pending           int        `json:"pending"`
	Working           int        `json:"working"`
	Blocked           int        `json:"blocked"`
	FailedActive      int        `json:"failed_active"`
	Skipped           int        `json:"skipped"`
	Done              int        `json:"done"`
	ThroughputPerMin  float64    `json:"throughput_per_min"`
	LastCompletedAt   *time.Time `json:"last_completed_at,omitempty"`
}

// Health aggregates the dashboard payload. Per-call inputs (Enabled,
// Probe) come from the caller; persistent state comes from the service's
// repos.
func (s *Service) Health(ctx context.Context, caller owners.Principal, in HealthInput) Health {
	h := Health{Enabled: in.Enabled}
	if !in.Enabled {
		h.PausedReason = "config_disabled"
		return h
	}
	acked, err := s.deps.Ack.IsAcknowledged(ctx, caller)
	if err != nil || !acked {
		h.PausedReason = "acknowledgement_required"
	}
	now := time.Now().UTC()
	h.Vision.LastCheckAt = now
	if err := in.Probe.Probe(ctx); err == nil {
		h.Vision.Reachable = true
	} else {
		h.Vision.LastError = err.Error()
	}
	h.Tag = s.taskHealth(ctx, ai.TaskTag, s.deps.ConfigFingerprints.Tag)
	h.Caption = s.taskHealth(ctx, ai.TaskCaption, s.deps.ConfigFingerprints.Caption)
	return h
}

func (s *Service) taskHealth(ctx context.Context, t ai.Task, fp ai.Fingerprint) TaskPart {
	tp := TaskPart{ActiveFingerprint: fp.String()}
	if c, err := s.deps.Queue.Counters(ctx, t); err == nil {
		tp.Pending = c.Pending
		tp.Working = c.Working
		tp.Blocked = c.Blocked
	}
	if n, err := s.deps.Failures.CountForFingerprint(ctx, t, fp); err == nil {
		tp.FailedActive = n
	}
	if n, err := s.deps.Skipped.Count(ctx, t); err == nil {
		tp.Skipped = n
	}
	if n, err := s.deps.Results.DoneCount(ctx, t, fp); err == nil {
		tp.Done = n
	}
	return tp
}

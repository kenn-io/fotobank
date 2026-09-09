package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/owners"
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
	Enabled              bool                         `json:"enabled"`
	PausedReason         string                       `json:"paused_reason"`
	Vision               VisionPart                   `json:"vision"`
	Tag                  TaskPart                     `json:"tag"`
	Caption              TaskPart                     `json:"caption"`
	Embed                EmbedTaskPart                `json:"embed"`
	EmbeddingGenerations []EmbeddingGenerationSummary `json:"embedding_generations"`
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

// EmbedTaskPart is the embed-task sub-block. It mirrors TaskPart's
// shape so the panel can reuse the tag/caption renderer, plus a
// PausedReason field. PausedReason carries the embed-specific pause
// signal "acknowledgement_required" — emitted when the caller has not
// yet acknowledged hidden processing, which gates the activator (and
// the embed worker's owner-scoped enqueue path). Distinct from
// Health.PausedReason because tag/caption stay unblocked under the
// same condition: their workers park individual jobs as 'blocked'
// rather than freezing the whole task pipeline.
type EmbedTaskPart struct {
	TaskPart
	PausedReason string      `json:"paused_reason"`
	Provider     *VisionPart `json:"provider,omitempty"`
}

// EmbeddingGenerationSummary is the per-generation row surfaced on the
// AI panel. Mirrors embedding.Row's stable fields but expressed in the
// shape the panel renders: Fingerprint as the canonical string,
// EmbeddedCount recounted under the activator's hidden predicate (so
// the displayed value matches the activation decision), EligibleCount
// computed from the same predicate. ActivatedAt and RetiredAt are
// nullable because building rows have neither set.
type EmbeddingGenerationSummary struct {
	ID            int64      `json:"id"`
	Fingerprint   string     `json:"fingerprint"`
	State         string     `json:"state"` // building | active | retired
	EmbeddedCount int        `json:"embedded_count"`
	EligibleCount int        `json:"eligible_count"`
	CreatedAt     time.Time  `json:"created_at"`
	ActivatedAt   *time.Time `json:"activated_at,omitempty"`
	RetiredAt     *time.Time `json:"retired_at,omitempty"`
}

// EmbeddingActivatorIface is the minimal surface the health aggregator
// needs to compute eligible/embedded counts under the activator's own
// hidden predicate. The concrete type is *embedding.Activator;
// declaring it as an interface keeps service/ai from importing the
// embedding package directly.
type EmbeddingActivatorIface interface {
	EligibleCount(ctx context.Context) (int, error)
	EmbeddedCount(ctx context.Context, generationID int64) (int, error)
}

// EmbeddingGenerationsLister is the minimal surface for listing
// generation rows from the registry. Production wiring is
// SQLEmbeddingGenerationsLister (defined below); tests substitute a
// fake. The aggregator doesn't need the full embedding.Generations
// repo — only an enumeration of the rows in a state.
type EmbeddingGenerationsLister interface {
	List(ctx context.Context, state string) ([]EmbeddingGenerationRow, error)
}

// EmbeddingGenerationRow is the structural type the aggregator reads
// directly from embedding_generations. We re-declare the minimal subset
// here (rather than importing embedding.Row) so the service package
// doesn't take on the embedding package's dependency surface for a
// read-only health-payload concern. The columns are stable per the §6.4
// schema.
type EmbeddingGenerationRow struct {
	ID            int64
	Fingerprint   string
	State         string
	EmbeddedCount int
	CreatedAt     time.Time
	ActivatedAt   *time.Time
	RetiredAt     *time.Time
}

// Health aggregates the dashboard payload. Per-call inputs (Enabled,
// Probe) come from the caller; persistent state comes from the service's
// repos.
func (s *Service) Health(ctx context.Context, caller owners.Principal, in HealthInput) Health {
	enabled := in.Enabled
	if s.deps.Runtime != nil {
		enabled = s.deps.Runtime.Effective().Config.Enabled
	}
	h := Health{Enabled: enabled}
	if !enabled {
		h.PausedReason = "config_disabled"
		return h
	}
	acked, err := s.deps.Ack.IsAcknowledged(ctx, caller)
	if err != nil || !acked {
		h.PausedReason = "acknowledgement_required"
	}
	now := time.Now().UTC()
	h.Vision.LastCheckAt = now
	if in.Probe == nil {
		h.Vision.LastError = "probe not configured"
	} else if err := in.Probe.Probe(ctx); err == nil {
		h.Vision.Reachable = true
	} else {
		h.Vision.LastError = err.Error()
	}
	tagFP, _ := s.taskFingerprints(ai.TaskTag)
	captionFP, _ := s.taskFingerprints(ai.TaskCaption)
	h.Tag = s.taskHealth(ctx, caller, ai.TaskTag, tagFP.result)
	h.Caption = s.taskHealth(ctx, caller, ai.TaskCaption, captionFP.result)
	h.Embed = s.embedHealth(ctx, caller, !acked)
	if s.deps.Runtime != nil && s.deps.Runtime.Effective().Config.Embed.Enabled && s.deps.EmbeddingProbe != nil {
		provider := s.deps.EmbeddingProbe(ctx)
		h.Embed.Provider = &provider
	}
	h.EmbeddingGenerations = s.embeddingGenerations(ctx)
	return h
}

func (s *Service) taskHealth(ctx context.Context, caller owners.Principal, t ai.Task, fp ai.Fingerprint) TaskPart {
	tp := TaskPart{ActiveFingerprint: fp.String()}
	if c, err := s.deps.Queue.CountersByOwner(ctx, t, caller.Hub, caller.UserID); err == nil {
		tp.Pending = c.Pending
		tp.Working = c.Working
		tp.Blocked = c.Blocked
	}
	if n, err := s.deps.Failures.CountForFingerprintByOwner(ctx, t, fp, caller.Hub, caller.UserID); err == nil {
		tp.FailedActive = n
	}
	if n, err := s.deps.Skipped.CountByOwner(ctx, t, caller.Hub, caller.UserID); err == nil {
		tp.Skipped = n
	}
	if n, err := s.deps.Results.DoneCountByOwner(ctx, t, fp, caller.Hub, caller.UserID); err == nil {
		tp.Done = n
	}
	return tp
}

// embedHealth builds the EmbedTaskPart. taskHealth is shared (by way
// of the embedded TaskPart) but the embed surface omits Done — embed
// completion is tracked through the embedding_generations registry,
// not ai_results. Failures and skipped counts use the embed
// fingerprint from ConfigFingerprints. ackMissing is the parent
// Health-level acknowledgement signal; mirroring it onto PausedReason
// keeps the panel's "embed paused" pill in lockstep with the activator
// (which short-circuits Tick on the same condition) without a second
// round-trip to the ack store.
func (s *Service) embedHealth(ctx context.Context, caller owners.Principal, ackMissing bool) EmbedTaskPart {
	fp, _ := s.taskFingerprints(ai.TaskEmbed)
	tp := EmbedTaskPart{ActiveFingerprint: fp.result.String()}
	if ackMissing {
		tp.PausedReason = "acknowledgement_required"
	}
	if c, err := s.deps.Queue.CountersByOwner(ctx, ai.TaskEmbed, caller.Hub, caller.UserID); err == nil {
		tp.Pending = c.Pending
		tp.Working = c.Working
		tp.Blocked = c.Blocked
	}
	if n, err := s.deps.Failures.CountForFingerprintByOwner(ctx, ai.TaskEmbed, fp.result, caller.Hub, caller.UserID); err == nil {
		tp.FailedActive = n
	}
	if n, err := s.deps.Skipped.CountByOwner(ctx, ai.TaskEmbed, caller.Hub, caller.UserID); err == nil {
		tp.Skipped = n
	}
	return tp
}

// embeddingGenerations returns the building+active generation rows for
// the panel's "embedding generations" block. Retired rows are
// intentionally excluded — they're a compactor concern, not an
// activation-status concern, and surfacing them on the live panel adds
// noise the user can't act on.
//
// EligibleCount is computed via the activator's exported helper so the
// panel's "embedded / eligible" reading matches the activation
// decision. EmbeddedCount is also recounted (rather than read from the
// cached column) so the same JOIN-derived predicate that gates
// promotion gates display.
//
// When the embed surface is not wired (empty deps), the slice is empty
// — callers see the same shape with a zero-length list, not a nil.
func (s *Service) embeddingGenerations(ctx context.Context) []EmbeddingGenerationSummary {
	if s.deps.EmbeddingGenerations == nil {
		return []EmbeddingGenerationSummary{}
	}
	rows := make([]EmbeddingGenerationRow, 0)
	for _, state := range []string{"building", "active"} {
		batch, err := s.deps.EmbeddingGenerations.List(ctx, state)
		if err != nil {
			// Health is best-effort — a transient SQL hiccup must not
			// fail the whole payload. The panel tolerates an empty
			// generations slice and renders the rest of the surface.
			continue
		}
		rows = append(rows, batch...)
	}

	// Eligible count is global per principal, so compute it once and
	// reuse across rows. If the activator isn't wired (or the count
	// query fails), eligible falls back to 0 — the panel shows
	// "0 eligible" rather than rendering a wrong number.
	eligible := 0
	if s.deps.EmbeddingActivator != nil {
		if n, err := s.deps.EmbeddingActivator.EligibleCount(ctx); err == nil {
			eligible = n
		}
	}

	out := make([]EmbeddingGenerationSummary, 0, len(rows))
	for _, r := range rows {
		summary := EmbeddingGenerationSummary{
			ID:            r.ID,
			Fingerprint:   r.Fingerprint,
			State:         r.State,
			EmbeddedCount: r.EmbeddedCount,
			EligibleCount: eligible,
			CreatedAt:     r.CreatedAt,
			ActivatedAt:   r.ActivatedAt,
			RetiredAt:     r.RetiredAt,
		}
		// Recount embedded under the activator's hidden predicate so the
		// panel's number matches the activation decision exactly. The
		// cached column on embedding_generations.embedded_count can drift
		// (e.g. when a previously-mapped media is hidden after the fact);
		// see TestActivator_AssertiveCount_IgnoresOrphanedMappings.
		if s.deps.EmbeddingActivator != nil {
			if n, err := s.deps.EmbeddingActivator.EmbeddedCount(ctx, r.ID); err == nil {
				summary.EmbeddedCount = n
			}
		}
		out = append(out, summary)
	}
	return out
}

// scanGenerationRow reads one embedding_generations row. Lives in
// service/ai (rather than being imported from embedding) to keep the
// dependency arrow one-way: service/ai depends on the schema (which is
// stable), not on the embedding package's wider surface.
func scanGenerationRow(s interface {
	Scan(dest ...any) error
}) (EmbeddingGenerationRow, error) {
	var (
		row         EmbeddingGenerationRow
		activatedAt sql.NullTime
		retiredAt   sql.NullTime
	)
	if err := s.Scan(&row.ID, &row.Fingerprint, &row.State,
		&row.EmbeddedCount, &row.CreatedAt, &activatedAt, &retiredAt); err != nil {
		return EmbeddingGenerationRow{}, err
	}
	if activatedAt.Valid {
		v := activatedAt.Time
		row.ActivatedAt = &v
	}
	if retiredAt.Valid {
		v := retiredAt.Time
		row.RetiredAt = &v
	}
	return row, nil
}

// SQLEmbeddingGenerationsLister is the production-side adapter that
// reads embedding_generations rows directly from the read-only pool.
// Server composition constructs it to satisfy EmbeddingGenerationsLister.
type SQLEmbeddingGenerationsLister struct {
	RO *sql.DB
}

// List reads rows from embedding_generations under the supplied state.
// We use a direct SQL read instead of routing through
// embedding.Generations.List because the health surface only needs the
// small subset declared on EmbeddingGenerationRow — re-declaring the
// columns here keeps the service package free of an embedding import.
func (l *SQLEmbeddingGenerationsLister) List(ctx context.Context, state string) ([]EmbeddingGenerationRow, error) {
	if l == nil || l.RO == nil {
		return nil, errors.New("SQLEmbeddingGenerationsLister.RO not configured")
	}
	rows, err := l.RO.QueryContext(ctx, `
		SELECT id, fingerprint, state, embedded_count, created_at, activated_at, retired_at
		  FROM embedding_generations
		 WHERE state = ?
		 ORDER BY id ASC`, state)
	if err != nil {
		return nil, fmt.Errorf("list generations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []EmbeddingGenerationRow
	for rows.Next() {
		row, err := scanGenerationRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter: %w", err)
	}
	return out, nil
}

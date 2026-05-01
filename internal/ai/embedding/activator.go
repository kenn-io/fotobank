package embedding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
)

// ActivatorCfg parametrises the activator. ThresholdPct is the
// activation cutoff (the building generation flips to active once
// embedded/eligible >= ThresholdPct/100). Tick is consumed by the H2
// Run loop — the H1 surface only invokes Tick() directly.
type ActivatorCfg struct {
	// Principal is the owner whose media participates in the eligible
	// and embedded counts. v1 is single-principal: the activator runs
	// once for the configured stub-mode owner. Multi-principal
	// fan-out is a post-v1 concern.
	Principal owners.Principal
	// ThresholdPct is the percentage cutoff (0..100) at which a
	// building generation is promoted. The plan default is 95.
	ThresholdPct int
	// Tick is the polling cadence used by Run (Task H2). The H1 Tick
	// method runs one pass synchronously regardless of this field.
	Tick time.Duration
}

// Activator is the post-build watcher that promotes a building
// generation once the embedded/eligible ratio crosses
// cfg.ThresholdPct. It owns no goroutines on its own — the caller
// drives a single pass via Tick, or H2 wires the long-running Run loop.
//
// The two SQL queries (eligible / embedded) are intentionally derived
// from the §6.6 hidden-photo predicate so the activator's measure of
// "complete" matches the gap scanner's measure of "still has work".
// The eligibility predicate also gates on ai_skipped — videos that
// were marked skipped at intake (Task I1) don't count toward the
// activation budget.
//
// All writes (the Promote call) flow through Generations, which
// already owns the rw pool — so the activator only needs the ro
// pool for its own counting queries.
//
// Concurrency: Tick is single-threaded by contract. Two activator
// goroutines would race on FindBuilding → Promote; v1 wires exactly
// one activator per server. H2's Run loop preserves that contract by
// awaiting each Tick before scheduling the next.
type Activator struct {
	ro     *sql.DB
	gens   *Generations
	ack    *ack.Store
	cfg    ActivatorCfg
	events EventEmitter
}

// NewActivator constructs an Activator. The rw pool is consumed
// transitively through gens (Promote writes via gens.rw); the
// activator's own queries are read-only and route to ro. events
// defaults to NoopEmitter when nil so the simplest test wiring stays
// terse — the worker uses the same convention.
func NewActivator(ro *sql.DB, gens *Generations, a *ack.Store, events EventEmitter, cfg ActivatorCfg) *Activator {
	if events == nil {
		events = NoopEmitter{}
	}
	return &Activator{
		ro:     ro,
		gens:   gens,
		ack:    a,
		cfg:    cfg,
		events: events,
	}
}

// Run is the long-running activator loop driver. Each cfg.Tick it
// invokes Tick once and logs (does not propagate) per-tick errors so a
// transient SQL hiccup does not tear down the watcher. Returns nil on
// ctx cancellation — matches the embed worker's Run shape so the server
// can shut both down via the same context.
//
// Concurrency: Run is single-threaded by contract. v1 wires exactly
// one activator per server (see activator.go file-level comment); the
// loop preserves that contract by awaiting each Tick before scheduling
// the next.
func (a *Activator) Run(ctx context.Context) error {
	t := time.NewTicker(a.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := a.Tick(ctx); err != nil {
				// Doctrine: log and continue. A transient SQL error
				// here (e.g. a brief lock contention spike) must not
				// stop the activator — the next tick re-evaluates from
				// scratch and the failed pass had no side effects
				// because every write flows through Promote inside a
				// single tx that rolls back on error.
				//
				// context.Canceled is the shutdown signal racing with a
				// tick that was already in flight when the caller
				// canceled; suppress it because the next loop iteration
				// will observe ctx.Done() and return nil cleanly.
				if !errors.Is(err, context.Canceled) {
					slog.Default().Warn("embedding activator tick failed", "err", err)
				}
			}
		}
	}
}

// Tick runs one activation pass:
//
//  1. If the principal has not acknowledged hidden processing, the
//     activator is paused — no-op.
//  2. If no building generation exists, no-op.
//  3. Compute eligible_count (visible-media count under the §6.6
//     hidden predicate) and embedded_count (assertive recount via JOIN
//     to media — the cached embedded_generations.embedded_count is not
//     trusted for the activation decision).
//  4. If eligible == 0, no-op (avoid a divide-by-zero and the
//     "promote on empty library" pathology).
//  5. If embedded * 100 / eligible >= cfg.ThresholdPct, promote and
//     emit the activation event.
//
// All branches return nil on the no-op path; only infrastructure
// failures (SQL errors, Promote errors) bubble up.
//
// The activator deliberately uses ackAllowsHidden=false on both
// queries: in v1, the activator's "complete" measure doesn't change
// based on the principal's ack state — hidden media isn't part of the
// activation budget regardless. The chat/embed worker handles per-job
// ack gating; the activator is a separate axis (the principal's pause
// signal). Threading through the principal's ack state would make the
// promote condition flicker as users toggle their hidden-processing
// preference, which is the wrong UX.
func (a *Activator) Tick(ctx context.Context) error {
	// 1. Ack gate. IsAcknowledged returns true once the principal has
	// recorded an acknowledgement; "required and missing" maps to
	// !isAck → no-op.
	isAck, err := a.ack.IsAcknowledged(ctx, a.cfg.Principal)
	if err != nil {
		return fmt.Errorf("ack lookup: %w", err)
	}
	if !isAck {
		return nil
	}

	// 2. Find the oldest building generation. Nil means no candidate
	// is in flight; nothing to do.
	building, err := a.gens.FindBuilding(ctx)
	if err != nil {
		return fmt.Errorf("find building: %w", err)
	}
	if building == nil {
		return nil
	}

	// 3. Recount eligible + embedded under the hidden predicate. Both
	// queries see ackAllowsHidden=false so the activator's measure
	// doesn't shift with the principal's preference (see method-level
	// comment).
	const ackAllowsHidden = false
	eligible, err := a.eligibleCount(ctx, ackAllowsHidden)
	if err != nil {
		return fmt.Errorf("eligible count: %w", err)
	}
	if eligible == 0 {
		// Empty library (or every photo skipped): no activation
		// budget to measure against. Distinct from the threshold
		// branch because integer division would otherwise be a
		// divide-by-zero.
		return nil
	}
	embedded, err := a.embeddedCount(ctx, building.ID, ackAllowsHidden)
	if err != nil {
		return fmt.Errorf("embedded count: %w", err)
	}

	// 4. Threshold check. Integer arithmetic — embedded * 100 /
	// eligible never overflows for plausible library sizes (max int64
	// vs library counts in the millions), and the truncation toward
	// zero only ever undercounts the ratio, which is the correct
	// direction (we'd rather wait one extra tick than promote too
	// early).
	if embedded*100/eligible < a.cfg.ThresholdPct {
		return nil
	}

	// 5. Promote. Promote is idempotent under a normal racing
	// activator (a second tick after promotion finds the row
	// already-active and FindBuilding returns nil), but a
	// concurrent Retire on the same id between FindBuilding and
	// Promote would surface as ErrNotFound — treat that as a no-op
	// because the row is no longer a promotion candidate.
	if err := a.gens.Promote(ctx, building.ID); err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("promote %d: %w", building.ID, err)
	}
	a.events.EmitAIEmbedGenerationActivated(building.ID, building.Fingerprint)
	return nil
}

// EligibleCount returns the eligible-media count under the §6.6 hidden
// predicate (ackAllowsHidden=false), exposing the activator's own
// counter to external callers — specifically the health aggregator,
// which needs the same number to populate the embedding-generation
// summary block. Reusing the activator's SQL keeps the panel's
// "embedded / eligible" reading in lockstep with the activation
// decision.
func (a *Activator) EligibleCount(ctx context.Context) (int, error) {
	return a.eligibleCount(ctx, false)
}

// EmbeddedCount returns the assertive (JOIN-derived) embedded-media
// count for generationID under the §6.6 hidden predicate
// (ackAllowsHidden=false). Mirrors EligibleCount for the same
// rationale: the health aggregator needs the same recount the
// activator's promote condition is built on.
func (a *Activator) EmbeddedCount(ctx context.Context, generationID int64) (int, error) {
	return a.embeddedCount(ctx, generationID, false)
}

// eligibleCount returns the count of media owned by the configured
// principal that are eligible for embedding under the §6.6 hidden
// predicate. The query mirrors the gap scanner's eligibility filter,
// minus the per-generation mapping check (eligible counts every
// candidate regardless of which generations have already embedded it).
func (a *Activator) eligibleCount(ctx context.Context, ackAllowsHidden bool) (int, error) {
	var n int
	err := a.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM media m
		 WHERE m.owner_hub = ? AND m.owner_user_id = ?
		   AND m.thumb_status = 'ready'
		   AND (m.hidden_at IS NULL OR ?)
		   AND NOT EXISTS (
		     SELECT 1 FROM ai_skipped sk
		      WHERE sk.media_id = m.id AND sk.task = 'embed'
		   )`,
		a.cfg.Principal.Hub, a.cfg.Principal.UserID, ackAllowsHidden,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("scan: %w", err)
	}
	return n, nil
}

// embeddedCount returns the count of mappings in the supplied building
// generation whose media row still satisfies the eligibility
// predicate. The JOIN to media is the assertive part — the activator
// does not trust the cached embedding_generations.embedded_count
// column, which can drift from reality (e.g. a media row that became
// hidden after its mapping was written would still inflate the
// counter). Recounting on every tick is cheap (the indexes on media
// and the PK on media_embedding_ids cover the join) and keeps the
// activation decision honest.
func (a *Activator) embeddedCount(ctx context.Context, generationID int64, ackAllowsHidden bool) (int, error) {
	var n int
	err := a.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM media_embedding_ids x
		  JOIN media m ON m.id = x.media_id
		 WHERE x.generation_id = ?
		   AND m.owner_hub = ? AND m.owner_user_id = ?
		   AND m.thumb_status = 'ready'
		   AND (m.hidden_at IS NULL OR ?)
		   AND NOT EXISTS (
		     SELECT 1 FROM ai_skipped sk
		      WHERE sk.media_id = m.id AND sk.task = 'embed'
		   )`,
		generationID,
		a.cfg.Principal.Hub, a.cfg.Principal.UserID, ackAllowsHidden,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("scan: %w", err)
	}
	return n, nil
}

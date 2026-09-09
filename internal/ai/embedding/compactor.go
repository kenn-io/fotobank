package embedding

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Compactor drops retired generations whose retired_at is older than
// the configured retain window. It pairs with the activator: once a
// generation has been retired long enough that no admin would still
// want to inspect or re-promote it, the compactor reclaims its vec0
// table and registry row so the database does not grow unbounded
// across model rolls.
//
// The compactor uses the rw pool directly. SweepOnce runs entirely
// outside the gens repo to avoid coupling housekeeping behaviour to
// the registry abstraction; the per-target work (DROP TABLE + DELETE
// row) sequences inside its own transaction so a partial sweep never
// leaves a vec0 table without a backing registry row, or vice versa.
//
// The DELETE on embedding_generations cascades through the
// media_embedding_ids FK (ON DELETE CASCADE), so mapping rows tied to
// the dropped generation are cleaned up automatically inside the same
// tx — no separate DELETE for the mapping table is required.
type Compactor struct {
	rw     *sql.DB
	window time.Duration
}

// NewCompactor builds a Compactor that drops retired generations whose
// retired_at < now - retainRetired. retainRetired comes from the
// [search] retain_retired_days config (B1) — converted to a duration by
// the caller — so an operator's "keep retired generations for N days"
// preference flows through unchanged.
func NewCompactor(rw *sql.DB, retainRetired time.Duration) *Compactor {
	return &Compactor{rw: rw, window: retainRetired}
}

// SweepOnce drops every retired generation whose retired_at is older
// than now - window. Returns the count of generations that were fully
// compacted (registry row removed AND vec0 table dropped).
//
// On a per-target failure SweepOnce returns the partial count up to
// the failing target along with the error. Earlier successes are
// already committed and will not be undone — each generation lives in
// its own short tx, so the compactor degrades gracefully under
// transient lock contention or a missing vec0 table that can't be
// dropped.
//
// Concurrency: a transient writer holding the rw connection (e.g. the
// embed worker mid-claim) will queue this writer behind it; SQLite's
// 5-second busy timeout keeps the wait bounded. SweepOnce is intended
// to run on a daily-tick goroutine, so light contention is expected
// and acceptable.
func (c *Compactor) SweepOnce(ctx context.Context) (int, error) {
	cutoff := time.Now().UTC().Add(-c.window)
	targets, err := c.candidates(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	dropped := 0
	for _, target := range targets {
		ok, err := c.dropOne(ctx, target.ID, target.VecTableName, cutoff)
		if err != nil {
			return dropped, fmt.Errorf("drop generation %d: %w", target.ID, err)
		}
		if ok {
			dropped++
		}
	}
	return dropped, nil
}

type CompactCandidate struct {
	ID           int64     `json:"id"`
	VecTableName string    `json:"vec_table_name"`
	RetiredAt    time.Time `json:"retired_at"`
}

// Candidates uses the sweep's predicate without changing any data.
func (c *Compactor) Candidates(ctx context.Context) ([]CompactCandidate, error) {
	return c.candidates(ctx, time.Now().UTC().Add(-c.window))
}

func (c *Compactor) candidates(ctx context.Context, cutoff time.Time) ([]CompactCandidate, error) {
	rows, err := c.rw.QueryContext(ctx,
		`SELECT id, vec_table_name, retired_at FROM embedding_generations
		 WHERE state='retired' AND retired_at < ? ORDER BY id ASC`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("select retired generations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	targets := []CompactCandidate{}
	for rows.Next() {
		var target CompactCandidate
		if err := rows.Scan(&target.ID, &target.VecTableName, &target.RetiredAt); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close before SweepOnce starts its per-target transactions on the same pool.
	return targets, rows.Close()
}

// dropOne removes one retired generation: DELETE the registry row
// guarded by state='retired' AND retired_at<cutoff, then DROP TABLE on
// its vec0 virtual table — but only if the DELETE actually affected
// a row. Returns (true, nil) when the registry row was removed AND
// the table dropped; (false, nil) when the row no longer matches the
// guard (re-promoted between phase 1 and phase 2 of SweepOnce).
//
// Order matters: DELETE first with the state guard, then DROP. If
// the DELETE matches zero rows, the operator has re-promoted the
// generation (state='active' or 'building') in the window between
// the SELECT and the DELETE — keeping the vec0 table is correct in
// that case. Both statements run inside one tx so a rollback
// undoes the DELETE if the DROP fails.
//
// The vec_table_name column is application-derived
// ("media_embeddings_g{id}") at insert time and never user-supplied,
// so interpolating it into the DROP statement is safe. DROP TABLE IF
// EXISTS is idempotent — a missing vec0 table (e.g. from a prior
// half-compacted target where the registry row never made it to
// disk) does not fail the tx.
//
// FK ON DELETE CASCADE on media_embedding_ids drops the mapping
// rows in the same statement as the registry DELETE. PRAGMA
// foreign_keys=ON is set in the DSN (internal/db/db.go), so the
// cascade is reliable.
func (c *Compactor) dropOne(ctx context.Context, id int64, vecTableName string, cutoff time.Time) (bool, error) {
	tx, err := c.rw.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM embedding_generations
		  WHERE id = ? AND state = 'retired' AND retired_at < ?`,
		id, cutoff,
	)
	if err != nil {
		return false, fmt.Errorf("delete generation row: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		// Row was re-promoted (state != 'retired') or its retired_at
		// shifted forward past cutoff between phase 1 and phase 2 of
		// SweepOnce. Don't drop the vec table — its data is back in
		// service. Commit (or roll back) the empty tx and report
		// "not dropped" so the caller doesn't increment its counter.
		return false, nil
	}

	dropSQL := fmt.Sprintf(`DROP TABLE IF EXISTS %s`, vecTableName)
	if _, err := tx.ExecContext(ctx, dropSQL); err != nil {
		return false, fmt.Errorf("drop vec table %s: %w", vecTableName, err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

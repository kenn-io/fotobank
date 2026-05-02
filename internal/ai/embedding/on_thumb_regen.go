package embedding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/ai"
)

// OnThumbRegen drops every (gen, media) mapping in non-retired
// generations, drops the corresponding vec0 row, decrements each
// affected gen's cached embedded_count, AND supersedes any in-flight
// embed jobs for this media so a worker mid-flight on the prior
// (now-stale) preview rolls back its commit instead of writing a
// vector keyed to a thumbnail that no longer exists. The supersede
// flips ai_jobs rows to status='superseded', which the worker's
// markDoneTx claim-fence (status='working' AND claimed_at=?) catches
// — the rows-affected=0 path returns ErrClaimLost and the worker's
// outer tx rolls back.
//
// The gap scanner re-enqueues embed jobs on its next tick —
// OnThumbRegen does NOT enqueue inside this transaction because the
// spec keeps the regen→re-embed boundary clean.
//
// Must be called inside the same write transaction that finalises a
// regenerated thumbnail (i.e. the MarkReady UPDATE). The 'retired'
// state is intentionally excluded for the mapping drop: retired
// generations are frozen and never re-embedded, so leaving their
// stale (older-thumb) vectors in place is correct — those rows are
// scheduled for compaction by Task K1.
func OnThumbRegen(ctx context.Context, tx *sql.Tx, mediaID string) error {
	// List non-retired generations. We hold the rows open just long
	// enough to copy ids + table names — the per-gen DELETEs below run
	// against a slice copy so a long DELETE/UPDATE chain doesn't block
	// the cursor inside SQLite's single writer.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, vec_table_name FROM embedding_generations
		  WHERE state IN ('active','building')`)
	if err != nil {
		return fmt.Errorf("list non-retired generations: %w", err)
	}
	type genTarget struct {
		id    int64
		table string
	}
	var targets []genTarget
	for rows.Next() {
		var t genTarget
		if err := rows.Scan(&t.id, &t.table); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan generation target: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iter generation targets: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close generation cursor: %w", err)
	}

	invalidated := 0
	for _, tgt := range targets {
		// Drop the mapping row and capture the prior vec_id (if any).
		// DELETE ... RETURNING with no matching rows surfaces as
		// sql.ErrNoRows from Scan — that's the "media never mapped in
		// this gen" case, which is a no-op and not an error.
		var prior sql.NullInt64
		row := tx.QueryRowContext(ctx,
			`DELETE FROM media_embedding_ids
			   WHERE generation_id = ? AND media_id = ?
			   RETURNING vec_id`,
			tgt.id, mediaID,
		)
		switch err := row.Scan(&prior); {
		case errors.Is(err, sql.ErrNoRows):
			// No mapping in this gen — nothing to drop, no count change.
			continue
		case err != nil:
			return fmt.Errorf("drop mapping for gen %d: %w", tgt.id, err)
		}

		// Drop the vec0 row keyed by the captured vec_id. tgt.table is
		// application-derived ("media_embeddings_g{id}") and never
		// user-supplied, so interpolation is safe. prior.Valid must be
		// true here because the mapping schema requires a non-null
		// vec_id; a NULL would indicate corruption rather than a no-op.
		if !prior.Valid {
			return fmt.Errorf("gen %d: mapping row had NULL vec_id (schema corruption)", tgt.id)
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE vec_id = ?`, tgt.table),
			prior.Int64,
		); err != nil {
			return fmt.Errorf("drop vec row gen %d: %w", tgt.id, err)
		}

		// Decrement the cached embedded_count by exactly 1 — we only
		// reach here when a mapping row was actually removed. The
		// arithmetic is server-side so concurrent writers serialise on
		// the rw single-connection.
		if _, err := tx.ExecContext(ctx,
			`UPDATE embedding_generations
			    SET embedded_count = embedded_count - 1
			  WHERE id = ?`,
			tgt.id,
		); err != nil {
			return fmt.Errorf("dec embedded_count gen %d: %w", tgt.id, err)
		}
		invalidated++
	}

	now := time.Now().UTC()

	// Always supersede working in-flight workers. A worker that has
	// already CLAIMED an embed job (status='working') has resolved
	// the prior preview JPEG and is mid-flight on the encode/embed
	// call; even when no mappings existed yet (first-time embed
	// during the first thumb regen), letting that worker commit its
	// vector keyed to the now-stale preview would write the wrong
	// vector into the active generation.
	//
	// The worker's markDoneTx claim-fence (status='working' AND
	// claimed_at=?) sees status='superseded' here and the
	// rows-affected check returns ErrClaimLost — which rolls back
	// the worker's own tx. attempt_count is preserved so the panel
	// doesn't lose the prior accumulation; completed_at is stamped
	// to mirror the pending/blocked branch so terminal-state rows
	// always carry a completion timestamp regardless of which path
	// retired them.
	if _, err := tx.ExecContext(ctx,
		`UPDATE ai_jobs
		    SET status='superseded',
		        completed_at=?,
		        last_error_kind=?,
		        last_error='thumb_regenerated'
		  WHERE media_id=? AND task='embed' AND status='working'`,
		now, string(ai.ErrKindSuperseded), mediaID,
	); err != nil {
		return fmt.Errorf("supersede working embed jobs: %w", err)
	}

	// Supersede pending/blocked rows ONLY when at least one mapping
	// was actually invalidated. The first thumb-ready transition
	// reaches here too — the importer enqueues the embed job upfront
	// and the thumb worker fires this hook on the initial 'ready'
	// MarkReady, just like it does on a regen. In the first-thumb
	// case there are no mappings yet to invalidate; superseding the
	// legitimate pending embed job at that moment would force the
	// gap scanner to re-enqueue it. Gating on invalidated > 0 leaves
	// the first-time embed pipeline untouched while still rolling
	// back stale not-yet-claimed work after a real regeneration.
	if invalidated > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE ai_jobs
			    SET status='superseded',
			        completed_at=?,
			        last_error_kind=?,
			        last_error='thumb_regenerated'
			  WHERE media_id=? AND task='embed' AND status IN ('pending','blocked')`,
			now, string(ai.ErrKindSuperseded), mediaID,
		); err != nil {
			return fmt.Errorf("supersede pending/blocked embed jobs: %w", err)
		}
	}

	return nil
}

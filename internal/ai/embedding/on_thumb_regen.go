package embedding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// OnThumbRegen drops every (gen, media) mapping in non-retired generations,
// drops the corresponding vec0 row, and decrements each affected gen's
// cached embedded_count. The gap scanner re-enqueues embed jobs on its
// next tick — OnThumbRegen does NOT enqueue inside this transaction
// because the spec keeps the regen→re-embed boundary clean.
//
// Must be called inside the same write transaction that finalises a
// regenerated thumbnail (i.e. the MarkReady UPDATE). The 'retired' state
// is intentionally excluded: retired generations are frozen and never
// re-embedded, so leaving their stale (older-thumb) vectors in place is
// correct — those rows are scheduled for compaction by Task K1.
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
	}
	return nil
}

// Package skipped owns the ai_skipped table: provenance for media
// that won't be queued (videos at import time) or that were claimed
// then skipped (no_preview thumb state).
package skipped

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/ai"
)

// Repo is the DB-only handle for ai_skipped.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool and ro the
// reader pool; lookups are routed to ro.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// Record upserts a skip row. Calling Record twice for the same
// (media, task) is idempotent — reason updates in place.
func (r *Repo) Record(ctx context.Context, mediaID string, task ai.Task, reason string) error {
	_, err := r.rw.ExecContext(ctx, `
		INSERT INTO ai_skipped(media_id, task, reason, recorded_at) VALUES (?,?,?,?)
		ON CONFLICT(media_id, task) DO UPDATE SET
		  reason=excluded.reason, recorded_at=excluded.recorded_at`,
		mediaID, string(task), reason, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("upsert skip: %w", err)
	}
	return nil
}

// Get returns the skip reason for (media, task).
func (r *Repo) Get(ctx context.Context, mediaID string, task ai.Task) (string, bool, error) {
	row := r.ro.QueryRowContext(ctx,
		`SELECT reason FROM ai_skipped WHERE media_id=? AND task=?`, mediaID, string(task))
	var reason string
	switch err := row.Scan(&reason); {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("scan: %w", err)
	}
	return reason, true, nil
}

// Count returns how many media are skipped for a task (any reason).
func (r *Repo) Count(ctx context.Context, task ai.Task) (int, error) {
	row := r.ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_skipped WHERE task=?`, string(task))
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("scan: %w", err)
	}
	return n, nil
}

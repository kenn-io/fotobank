// Package failures owns the ai_failures table — the live record of
// per-media failures keyed by full fingerprint. Successful retry
// deletes the matching row; old-fingerprint rows simply stop
// appearing in current-fingerprint queries.
package failures

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/ai"
)

// Row is one current-fingerprint failure.
type Row struct {
	MediaID       string
	Task          ai.Task
	ModelID       string
	PromptVersion string
	InputProfile  string
	LastError     string
	LastErrorKind ai.LastErrorKind
	AttemptCount  int
	FailedAt      time.Time
}

// Repo is the DB-only handle for ai_failures.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool and ro the
// reader pool; list and count queries are routed to ro so panel
// reads do not contend with writers.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// Record upserts a failure row keyed by (media, task, fingerprint).
// On conflict, attempt_count is INCREMENTED (not overwritten) so a
// failure that re-enqueues — Enqueue creates a fresh ai_jobs row each
// time, with attempts starting at 0 — accumulates the running total
// across the whole retry history. Without the accumulator, every
// re-enqueue would reset attempt_count to 1 and the gap-scanner's
// failure-budget gate would never trip on a repeat-failing media.
//
// The attempts argument represents the just-failed run's attempt
// count for the FIRST insert (when no prior row exists). On conflict
// it is unused — the SET clause increments the prior value by 1.
func (r *Repo) Record(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint, kind ai.LastErrorKind, msg string, attempts int) error {
	_, err := r.rw.ExecContext(ctx, `
		INSERT INTO ai_failures
		  (media_id, task, model_id, prompt_version, input_profile,
		   last_error, last_error_kind, attempt_count, failed_at)
		VALUES (?,?,?,?,?, ?,?,?, ?)
		ON CONFLICT(media_id, task, model_id, prompt_version, input_profile) DO UPDATE SET
		  last_error      = excluded.last_error,
		  last_error_kind = excluded.last_error_kind,
		  attempt_count   = ai_failures.attempt_count + 1,
		  failed_at       = excluded.failed_at`,
		mediaID, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile,
		msg, string(kind), attempts, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("upsert failure: %w", err)
	}
	return nil
}

// Delete removes a single failure row (used after a successful retry).
func (r *Repo) Delete(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint) error {
	_, err := r.rw.ExecContext(ctx, `
		DELETE FROM ai_failures
		 WHERE media_id=? AND task=?
		   AND model_id=? AND prompt_version=? AND input_profile=?`,
		mediaID, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	if err != nil {
		return fmt.Errorf("delete failure: %w", err)
	}
	return nil
}

// DeleteTx removes the failure row inside an existing transaction. The
// worker uses this to clear the row in the same tx that writes the
// successful result and marks the job done, so a crash between the
// commit and a later cleanup can never leave a stale failure for a
// completed job.
func (r *Repo) DeleteTx(ctx context.Context, tx *sql.Tx, mediaID string, task ai.Task, fp ai.Fingerprint) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM ai_failures
		 WHERE media_id=? AND task=?
		   AND model_id=? AND prompt_version=? AND input_profile=?`,
		mediaID, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	if err != nil {
		return fmt.Errorf("delete failure tx: %w", err)
	}
	return nil
}

// DeleteAllForFingerprint clears all current-fingerprint failures for
// a task. Returns the number of rows deleted.
func (r *Repo) DeleteAllForFingerprint(ctx context.Context, task ai.Task, fp ai.Fingerprint) (int, error) {
	res, err := r.rw.ExecContext(ctx, `
		DELETE FROM ai_failures
		 WHERE task=? AND model_id=? AND prompt_version=? AND input_profile=?`,
		string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	if err != nil {
		return 0, fmt.Errorf("delete all: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetForFingerprint returns the failure row for (media, task, fp) if
// one exists. Used by the lightbox AI surface to display per-photo
// failure detail without scanning the full failures list.
func (r *Repo) GetForFingerprint(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint) (Row, bool, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT media_id, last_error, last_error_kind, attempt_count, failed_at
		  FROM ai_failures
		 WHERE media_id=? AND task=?
		   AND model_id=? AND prompt_version=? AND input_profile=?`,
		mediaID, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	out := Row{
		Task:          task,
		ModelID:       fp.ModelID,
		PromptVersion: fp.PromptVersion,
		InputProfile:  fp.InputProfile,
	}
	var kind string
	switch err := row.Scan(&out.MediaID, &out.LastError, &kind, &out.AttemptCount, &out.FailedAt); {
	case errors.Is(err, sql.ErrNoRows):
		return Row{}, false, nil
	case err != nil:
		return Row{}, false, fmt.Errorf("scan: %w", err)
	}
	out.LastErrorKind = ai.LastErrorKind(kind)
	return out, true, nil
}

// ListForFingerprint returns up to limit most-recent current-fingerprint
// failures, ordered by failed_at DESC.
func (r *Repo) ListForFingerprint(ctx context.Context, task ai.Task, fp ai.Fingerprint, limit int) ([]Row, error) {
	rows, err := r.ro.QueryContext(ctx, `
		SELECT media_id, last_error, last_error_kind, attempt_count, failed_at
		  FROM ai_failures
		 WHERE task=? AND model_id=? AND prompt_version=? AND input_profile=?
		 ORDER BY failed_at DESC
		 LIMIT ?`, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile, limit)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Row
	for rows.Next() {
		x := Row{
			Task:          task,
			ModelID:       fp.ModelID,
			PromptVersion: fp.PromptVersion,
			InputProfile:  fp.InputProfile,
		}
		var kind string
		if err := rows.Scan(&x.MediaID, &x.LastError, &kind, &x.AttemptCount, &x.FailedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		x.LastErrorKind = ai.LastErrorKind(kind)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// ListForFingerprintByOwner returns up to limit most-recent
// current-fingerprint failures whose media is owned by (hub, userID).
// limit <= 0 means unbounded — used by the retry-failed flow which
// must process every failure for the caller. cutoff filters to rows
// whose failed_at is <= cutoff so the retry loop sees a stable
// snapshot from the moment it started; pass a zero time.Time to skip
// the filter (the periodic gap-scan and panel reads use this form).
func (r *Repo) ListForFingerprintByOwner(ctx context.Context, task ai.Task, fp ai.Fingerprint, hub, userID string, cutoff time.Time, limit int) ([]Row, error) {
	q := `
		SELECT f.media_id, f.last_error, f.last_error_kind, f.attempt_count, f.failed_at
		  FROM ai_failures f
		  JOIN media m ON m.id = f.media_id
		 WHERE f.task=? AND f.model_id=? AND f.prompt_version=? AND f.input_profile=?
		   AND m.owner_hub=? AND m.owner_user_id=?`
	args := []any{string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile, hub, userID}
	if !cutoff.IsZero() {
		q += ` AND f.failed_at <= ?`
		args = append(args, cutoff)
	}
	q += ` ORDER BY f.failed_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := r.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list by owner: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Row
	for rows.Next() {
		x := Row{
			Task:          task,
			ModelID:       fp.ModelID,
			PromptVersion: fp.PromptVersion,
			InputProfile:  fp.InputProfile,
		}
		var kind string
		if err := rows.Scan(&x.MediaID, &x.LastError, &kind, &x.AttemptCount, &x.FailedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		x.LastErrorKind = ai.LastErrorKind(kind)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// DeleteByMediaIDs removes current-fingerprint failure rows for the
// supplied media IDs only. Used by the retry-failed flow so we never
// touch failures whose media isn't in the caller's selected set.
// Chunks IDs to stay under SQLite's bind-variable limit.
func (r *Repo) DeleteByMediaIDs(ctx context.Context, task ai.Task, fp ai.Fingerprint, mediaIDs []string) (int, error) {
	if len(mediaIDs) == 0 {
		return 0, nil
	}
	const chunkSize = 250
	total := 0
	for start := 0; start < len(mediaIDs); start += chunkSize {
		end := min(start+chunkSize, len(mediaIDs))
		chunk := mediaIDs[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, 0, len(chunk)+4)
		for _, id := range chunk {
			args = append(args, id)
		}
		args = append(args, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
		res, err := r.rw.ExecContext(ctx, `
			DELETE FROM ai_failures
			 WHERE media_id IN (`+placeholders+`)
			   AND task=? AND model_id=? AND prompt_version=? AND input_profile=?`,
			args...)
		if err != nil {
			return total, fmt.Errorf("delete by ids: %w", err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// CountForFingerprint returns how many failures match the active fingerprint.
func (r *Repo) CountForFingerprint(ctx context.Context, task ai.Task, fp ai.Fingerprint) (int, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM ai_failures
		 WHERE task=? AND model_id=? AND prompt_version=? AND input_profile=?`,
		string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// CountForFingerprintByOwner is the owner-scoped variant used by the
// per-caller health surface.
func (r *Repo) CountForFingerprintByOwner(ctx context.Context, task ai.Task, fp ai.Fingerprint, hub, userID string) (int, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM ai_failures f
		  JOIN media m ON m.id = f.media_id
		 WHERE f.task=? AND f.model_id=? AND f.prompt_version=? AND f.input_profile=?
		   AND m.owner_hub=? AND m.owner_user_id=?`,
		string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile, hub, userID)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count by owner: %w", err)
	}
	return n, nil
}

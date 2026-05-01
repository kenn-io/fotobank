// Package failures owns the ai_failures table — the live record of
// per-media failures keyed by full fingerprint. Successful retry
// deletes the matching row; old-fingerprint rows simply stop
// appearing in current-fingerprint queries.
package failures

import (
	"context"
	"database/sql"
	"fmt"
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
func (r *Repo) Record(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint, kind ai.LastErrorKind, msg string, attempts int) error {
	_, err := r.rw.ExecContext(ctx, `
		INSERT INTO ai_failures
		  (media_id, task, model_id, prompt_version, input_profile,
		   last_error, last_error_kind, attempt_count, failed_at)
		VALUES (?,?,?,?,?, ?,?,?, ?)
		ON CONFLICT(media_id, task, model_id, prompt_version, input_profile) DO UPDATE SET
		  last_error      = excluded.last_error,
		  last_error_kind = excluded.last_error_kind,
		  attempt_count   = excluded.attempt_count,
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

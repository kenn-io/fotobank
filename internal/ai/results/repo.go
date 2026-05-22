// Package results owns the ai_results / media_tags / media_captions
// tables. Every WriteX is atomic: stale prior active + insert new active
// + insert child rows in one transaction.
package results

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/parse"
	"go.kenn.io/fotobank/internal/search/index"
)

// Repo is the DB-only handle.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool and ro the
// reader pool; reads (GetActive*, DoneCount, HasActiveForFingerprint)
// are routed to ro so panel queries do not contend with writers.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// CaptionRow is the active-caption read shape.
type CaptionRow struct {
	Text          string
	ModelID       string
	PromptVersion string
	InputProfile  string
	GeneratedAt   time.Time
}

// WriteTagResult atomically replaces the active tag result for media.
func (r *Repo) WriteTagResult(ctx context.Context, mediaID string, fp ai.Fingerprint, promptHash string, tags []parse.Tag) error {
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.WriteTagResultTx(ctx, tx, mediaID, fp, promptHash, tags); err != nil {
		return err
	}
	return tx.Commit()
}

// WriteCaptionResult atomically replaces the active caption result.
func (r *Repo) WriteCaptionResult(ctx context.Context, mediaID string, fp ai.Fingerprint, promptHash, text string) error {
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.WriteCaptionResultTx(ctx, tx, mediaID, fp, promptHash, text); err != nil {
		return err
	}
	return tx.Commit()
}

// WriteTagResultTx is the in-tx variant of WriteTagResult. The caller
// owns commit/rollback. Used by the worker to combine result write +
// claim finalization in a single transaction. The FTS corpus row is
// refreshed at the end of the same tx so the active-tag set the
// promotion just wrote is what search reads.
func (r *Repo) WriteTagResultTx(ctx context.Context, tx *sql.Tx, mediaID string, fp ai.Fingerprint, promptHash string, tags []parse.Tag) error {
	if err := writeWithChildrenTx(ctx, tx, mediaID, ai.TaskTag, fp, promptHash, func(ctx context.Context, tx *sql.Tx, resultID string) error {
		for _, t := range tags {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO media_tags(result_id, tag_key, tag_label, rank) VALUES (?,?,?,?)`,
				resultID, t.Key, t.Label, t.Rank); err != nil {
				return fmt.Errorf("insert tag: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := index.RefreshMediaFTS(ctx, tx, mediaID); err != nil {
		return fmt.Errorf("refresh media fts: %w", err)
	}
	return nil
}

// WriteCaptionResultTx is the in-tx variant of WriteCaptionResult. The
// FTS corpus row is refreshed at the end of the same tx so the active
// caption the promotion just wrote is what search reads.
func (r *Repo) WriteCaptionResultTx(ctx context.Context, tx *sql.Tx, mediaID string, fp ai.Fingerprint, promptHash, text string) error {
	if err := writeWithChildrenTx(ctx, tx, mediaID, ai.TaskCaption, fp, promptHash, func(ctx context.Context, tx *sql.Tx, resultID string) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO media_captions(result_id, text) VALUES (?, ?)`, resultID, text)
		return err
	}); err != nil {
		return err
	}
	if err := index.RefreshMediaFTS(ctx, tx, mediaID); err != nil {
		return fmt.Errorf("refresh media fts: %w", err)
	}
	return nil
}

func writeWithChildrenTx(
	ctx context.Context,
	tx *sql.Tx,
	mediaID string,
	task ai.Task,
	fp ai.Fingerprint,
	promptHash string,
	writeChildren func(context.Context, *sql.Tx, string) error,
) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE ai_results SET status='stale' WHERE media_id=? AND task=? AND status='active'`,
		mediaID, string(task)); err != nil {
		return fmt.Errorf("stale prior: %w", err)
	}
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ai_results(id, media_id, task, model_id, prompt_version, prompt_hash,
		 input_profile, status, generated_at) VALUES (?,?,?,?,?,?,?, 'active', ?)`,
		id, mediaID, string(task), fp.ModelID, fp.PromptVersion, promptHash, fp.InputProfile,
		time.Now().UTC()); err != nil {
		return fmt.Errorf("insert result: %w", err)
	}
	return writeChildren(ctx, tx, id)
}

// GetActiveTags returns the active-result tags for a media, ordered by rank.
func (r *Repo) GetActiveTags(ctx context.Context, mediaID string) ([]parse.Tag, error) {
	rows, err := r.ro.QueryContext(ctx, `
		SELECT t.tag_key, t.tag_label, t.rank
		  FROM media_tags t
		  JOIN ai_results r ON r.id = t.result_id
		 WHERE r.media_id = ? AND r.task = 'tag' AND r.status = 'active'
		 ORDER BY t.rank ASC`, mediaID)
	if err != nil {
		return nil, fmt.Errorf("query tags: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []parse.Tag
	for rows.Next() {
		var t parse.Tag
		if err := rows.Scan(&t.Key, &t.Label, &t.Rank); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tags rows: %w", err)
	}
	return out, nil
}

// GetActiveCaption returns the active caption for a media or (zero, false, nil).
func (r *Repo) GetActiveCaption(ctx context.Context, mediaID string) (CaptionRow, bool, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT c.text, r.model_id, r.prompt_version, r.input_profile, r.generated_at
		  FROM media_captions c
		  JOIN ai_results r ON r.id = c.result_id
		 WHERE r.media_id = ? AND r.task = 'caption' AND r.status = 'active'`, mediaID)
	var c CaptionRow
	switch err := row.Scan(&c.Text, &c.ModelID, &c.PromptVersion, &c.InputProfile, &c.GeneratedAt); {
	case errors.Is(err, sql.ErrNoRows):
		return CaptionRow{}, false, nil
	case err != nil:
		return CaptionRow{}, false, fmt.Errorf("scan caption: %w", err)
	}
	return c, true, nil
}

// DoneCount returns how many active results exist for (task, fp).
func (r *Repo) DoneCount(ctx context.Context, task ai.Task, fp ai.Fingerprint) (int, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM ai_results
		 WHERE task=? AND status='active'
		   AND model_id=? AND prompt_version=? AND input_profile=?`,
		string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// DoneCountByOwner is the owner-scoped variant used by the per-caller
// health surface.
func (r *Repo) DoneCountByOwner(ctx context.Context, task ai.Task, fp ai.Fingerprint, hub, userID string) (int, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM ai_results r
		  JOIN media m ON m.id = r.media_id
		 WHERE r.task=? AND r.status='active'
		   AND r.model_id=? AND r.prompt_version=? AND r.input_profile=?
		   AND m.owner_hub=? AND m.owner_user_id=?`,
		string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile, hub, userID)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count by owner: %w", err)
	}
	return n, nil
}

// HasActiveForFingerprint is used by the gap scanner to skip rows that
// already have a current-fingerprint active result.
func (r *Repo) HasActiveForFingerprint(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint) (bool, error) {
	row := r.ro.QueryRowContext(ctx, `
		SELECT 1 FROM ai_results
		 WHERE media_id=? AND task=? AND status='active'
		   AND model_id=? AND prompt_version=? AND input_profile=?`,
		mediaID, string(task), fp.ModelID, fp.PromptVersion, fp.InputProfile)
	var n int
	switch err := row.Scan(&n); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("scan: %w", err)
	}
	return true, nil
}

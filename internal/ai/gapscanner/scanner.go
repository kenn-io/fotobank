// Package gapscanner walks media rows and enqueues missing AI jobs
// for the active fingerprint per task. CLI (fotobank ai backfill) and
// the periodic server-side tick both call Scan.
package gapscanner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
)

// ScanRequest controls one scan invocation.
//
// When Owner is non-zero, candidates are restricted to media owned by
// that principal: an explicit MediaIDs entry that doesn't belong to
// Owner is reported as errs.ErrNotFound, and an unbounded scan only
// considers Owner's library.
type ScanRequest struct {
	Task        ai.Task
	Fingerprint ai.Fingerprint
	Owner       owners.Principal
	Force       bool     // include media that already have an active result for fp
	Limit       int      // 0 = unbounded
	MediaIDs    []string // 0 = all media; otherwise scoped subset
}

// Scanner walks media rows, classifies them, and enqueues jobs.
type Scanner struct {
	ro      *sql.DB
	q       *jobs.Queue
	results *results.Repo
	skipped *skipped.Repo
}

// New constructs a Scanner.
func New(ro *sql.DB, q *jobs.Queue, r *results.Repo, s *skipped.Repo) *Scanner {
	return &Scanner{ro: ro, q: q, results: r, skipped: s}
}

// Scan returns the number of jobs enqueued.
func (s *Scanner) Scan(ctx context.Context, req ScanRequest) (int, error) {
	if !req.Task.Valid() {
		return 0, fmt.Errorf("invalid task %q", req.Task)
	}
	mediaRows, err := s.candidates(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("candidates: %w", err)
	}
	enqueued := 0
	for _, m := range mediaRows {
		if m.MediaType == "video" {
			if err := s.skipped.Record(ctx, m.ID, req.Task, "video"); err != nil {
				return enqueued, fmt.Errorf("record video skip: %w", err)
			}
			continue
		}
		if !req.Force {
			skip, err := s.shouldSkip(ctx, m.ID, req.Task, req.Fingerprint)
			if err != nil {
				return enqueued, err
			}
			if skip {
				continue
			}
		}
		if err := s.q.Enqueue(ctx, m.ID, req.Task, req.Fingerprint); err != nil {
			return enqueued, fmt.Errorf("enqueue %s: %w", m.ID, err)
		}
		enqueued++
	}
	return enqueued, nil
}

// shouldSkip returns true if the (media, task) pair has a terminal
// state we shouldn't disturb on a non-force scan: an active result for
// the current fingerprint, a current-fingerprint failure, or a skipped
// row. Without these checks the periodic tick would chase the same
// failed/skipped media every interval.
func (s *Scanner) shouldSkip(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint) (bool, error) {
	hasActive, err := s.results.HasActiveForFingerprint(ctx, mediaID, task, fp)
	if err != nil {
		return false, fmt.Errorf("check active: %w", err)
	}
	if hasActive {
		return true, nil
	}
	hasFailure, err := s.hasFailureForFingerprint(ctx, mediaID, task, fp)
	if err != nil {
		return false, fmt.Errorf("check failure: %w", err)
	}
	if hasFailure {
		return true, nil
	}
	_, hasSkipped, err := s.skipped.Get(ctx, mediaID, task)
	if err != nil {
		return false, fmt.Errorf("check skipped: %w", err)
	}
	return hasSkipped, nil
}

// hasFailureForFingerprint reports whether ai_failures already has a
// terminal row for (media, task, fp). Used by shouldSkip; inline rather
// than a repo method to keep the failures package surface narrow.
func (s *Scanner) hasFailureForFingerprint(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint) (bool, error) {
	row := s.ro.QueryRowContext(ctx, `
		SELECT 1 FROM ai_failures
		 WHERE media_id=? AND task=?
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

type candidate struct {
	ID        string
	MediaType string
}

// candidates returns media to consider. With MediaIDs set, returns only
// those rows; otherwise scans up to Limit (or all) media. When req.Owner
// is non-zero, the query is restricted to that owner's media so a caller
// can never enqueue jobs for another principal's library.
//
// On the unbounded path (no MediaIDs) and when !Force, the SQL pre-filters
// out media that already have an active result, a current-fingerprint
// failure, or a skipped row for the requested task. This means LIMIT
// applies to "media that still need work," so a periodic tick with
// LIMIT=200 against a 10k-row library actually makes progress every
// interval rather than re-scanning the same 200 finished rows. Results
// are ordered by id for deterministic batching.
func (s *Scanner) candidates(ctx context.Context, req ScanRequest) ([]candidate, error) {
	scoped := !req.Owner.IsZero()
	if len(req.MediaIDs) > 0 {
		out := make([]candidate, 0, len(req.MediaIDs))
		for _, id := range req.MediaIDs {
			var (
				mt   string
				stmt = `SELECT media_type FROM media WHERE id=?`
				args = []any{id}
			)
			if scoped {
				stmt += ` AND owner_hub=? AND owner_user_id=?`
				args = append(args, req.Owner.Hub, req.Owner.UserID)
			}
			row := s.ro.QueryRowContext(ctx, stmt, args...)
			switch err := row.Scan(&mt); {
			case errors.Is(err, sql.ErrNoRows):
				return nil, fmt.Errorf("%w: media %s", errs.ErrNotFound, id)
			case err != nil:
				return nil, fmt.Errorf("read %s: %w", id, err)
			}
			out = append(out, candidate{ID: id, MediaType: mt})
		}
		return out, nil
	}
	q := `SELECT m.id, m.media_type FROM media m`
	args := []any{}
	conds := []string{}
	if scoped {
		conds = append(conds, `m.owner_hub=? AND m.owner_user_id=?`)
		args = append(args, req.Owner.Hub, req.Owner.UserID)
	}
	if !req.Force {
		// Exclude media that already have an active result for the
		// current fingerprint. Skipping in SQL (rather than per-row
		// HasActiveForFingerprint) means LIMIT counts unfinished work.
		conds = append(conds, `NOT EXISTS (
			SELECT 1 FROM ai_results r
			 WHERE r.media_id=m.id AND r.task=? AND r.status='active'
			   AND r.model_id=? AND r.prompt_version=? AND r.input_profile=?)`)
		args = append(args,
			string(req.Task),
			req.Fingerprint.ModelID, req.Fingerprint.PromptVersion, req.Fingerprint.InputProfile)
		// Exclude terminal failures for the current fingerprint —
		// otherwise the periodic tick would re-enqueue dead jobs every
		// interval. Force scans intentionally bypass this so an
		// operator-driven retry-failed flow still works.
		conds = append(conds, `NOT EXISTS (
			SELECT 1 FROM ai_failures f
			 WHERE f.media_id=m.id AND f.task=?
			   AND f.model_id=? AND f.prompt_version=? AND f.input_profile=?)`)
		args = append(args,
			string(req.Task),
			req.Fingerprint.ModelID, req.Fingerprint.PromptVersion, req.Fingerprint.InputProfile)
		// Exclude skipped media (videos, no_preview thumbs). Skipped is
		// fingerprint-independent: once skipped for a task, stays
		// skipped until an explicit force/clear.
		conds = append(conds, `NOT EXISTS (
			SELECT 1 FROM ai_skipped s
			 WHERE s.media_id=m.id AND s.task=?)`)
		args = append(args, string(req.Task))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY m.id`
	if req.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, req.Limit)
	}
	rows, err := s.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query media: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.MediaType); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("candidate rows: %w", err)
	}
	return out, nil
}

// Package gapscanner walks media rows and enqueues missing AI jobs
// for the active fingerprint per task. CLI (fotobank ai backfill) and
// the periodic server-side tick both call Scan.
package gapscanner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
			has, err := s.results.HasActiveForFingerprint(ctx, m.ID, req.Task, req.Fingerprint)
			if err != nil {
				return enqueued, fmt.Errorf("check active: %w", err)
			}
			if has {
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

type candidate struct {
	ID        string
	MediaType string
}

// candidates returns media to consider. With MediaIDs set, returns only
// those rows; otherwise scans up to Limit (or all) media. When req.Owner
// is non-zero, the query is restricted to that owner's media so a caller
// can never enqueue jobs for another principal's library.
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
	q := `SELECT id, media_type FROM media`
	args := []any{}
	if scoped {
		q += ` WHERE owner_hub=? AND owner_user_id=?`
		args = append(args, req.Owner.Hub, req.Owner.UserID)
	}
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

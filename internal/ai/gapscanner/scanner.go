// Package gapscanner walks media rows and enqueues missing AI jobs
// for the active fingerprint per task. CLI (fotobank ai backfill) and
// the periodic server-side tick both call Scan.
package gapscanner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
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
	Task              ai.Task
	ClaimFingerprint  string
	ResultFingerprint ai.Fingerprint
	// Fingerprint is the legacy combined claim/result fingerprint. New
	// callers should set ClaimFingerprint and ResultFingerprint.
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
	resultFP := req.resultFingerprint()
	claimFP := req.claimFingerprint()
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
			skip, err := s.shouldSkip(ctx, m.ID, req.Task, resultFP)
			if err != nil {
				return enqueued, err
			}
			if skip {
				continue
			}
		}
		if err := s.q.EnqueueClaim(ctx, m.ID, req.Task, claimFP); err != nil {
			return enqueued, fmt.Errorf("enqueue %s: %w", m.ID, err)
		}
		enqueued++
	}
	return enqueued, nil
}

func (req ScanRequest) resultFingerprint() ai.Fingerprint {
	if req.ResultFingerprint != (ai.Fingerprint{}) {
		return req.ResultFingerprint
	}
	return req.Fingerprint
}

func (req ScanRequest) claimFingerprint() string {
	if req.ClaimFingerprint != "" {
		return req.ClaimFingerprint
	}
	return req.resultFingerprint().String()
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
	resultFP := req.resultFingerprint()
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
			resultFP.ModelID, resultFP.PromptVersion, resultFP.InputProfile)
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
			resultFP.ModelID, resultFP.PromptVersion, resultFP.InputProfile)
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

// EmbedScanRequest controls one ScanEmbed invocation.
//
// The embed predicate diverges from tag/caption: eligibility joins on
// media_embedding_ids by generation_id (so re-running against a
// different generation legitimately re-enqueues the same media), and
// the failure-budget check is keyed on the embed fingerprint (whose
// PromptVersion is empty by construction). Limit=0 means unbounded;
// MediaIDs=nil means scan the whole owner's library.
type EmbedScanRequest struct {
	Owner             owners.Principal
	Generation        embedding.Row
	ClaimFingerprint  string
	ResultFingerprint ai.Fingerprint
	// Fingerprint is the legacy combined claim/result fingerprint. New
	// callers should set ClaimFingerprint and ResultFingerprint.
	Fingerprint     ai.Fingerprint
	AckAllowsHidden bool
	RetryBudget     int
	Limit           int
	MediaIDs        []string
}

// ScanEmbed enqueues TaskEmbed jobs for media that satisfy the §6.6
// gap-fill predicate against req.Generation. Returns the count of
// enqueued jobs.
//
// Eligibility (all conjunctive):
//
//   - m.thumb_status = 'ready' (preview must exist before we can embed)
//   - m.hidden_at IS NULL OR req.AckAllowsHidden
//   - no media_embedding_ids row for (req.Generation.ID, m.id)
//   - no ai_skipped row for (m.id, task='embed')
//   - no ai_failures row matching the embed fingerprint with
//     attempt_count >= RetryBudget
//   - no in-flight ai_jobs row in pending|working|blocked for
//     (m.id, task='embed')
//
// Unlike Scan, ScanEmbed does not perform a per-row "is this a video?"
// fix-up: media v1 only embeds photo previews, and the importer is
// expected to record videos as ai_skipped (task='embed') at intake
// (Task I1) so the skip predicate above excludes them. If a video
// somehow reaches this point unskipped, the worker's preview resolver
// will fail and the failure-budget predicate will eventually drop it
// out of the candidate set.
func (s *Scanner) ScanEmbed(ctx context.Context, req EmbedScanRequest) (int, error) {
	ids, err := s.embedCandidates(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("embed candidates: %w", err)
	}
	enqueued := 0
	claimFP := req.claimFingerprint()
	for _, id := range ids {
		if err := s.q.EnqueueClaim(ctx, id, ai.TaskEmbed, claimFP); err != nil {
			return enqueued, fmt.Errorf("enqueue %s: %w", id, err)
		}
		enqueued++
	}
	return enqueued, nil
}

func (req EmbedScanRequest) resultFingerprint() ai.Fingerprint {
	if req.ResultFingerprint != (ai.Fingerprint{}) {
		return req.ResultFingerprint
	}
	return req.Fingerprint
}

func (req EmbedScanRequest) claimFingerprint() string {
	if req.ClaimFingerprint != "" {
		return req.ClaimFingerprint
	}
	return req.resultFingerprint().String()
}

// embedCandidates returns media IDs eligible for embed gap-fill against
// req.Generation. Order is by media id ASC for deterministic batching;
// LIMIT applies to "media that still need work" so a periodic tick over
// a mostly-finished library makes progress every interval.
//
// When req.MediaIDs is non-empty the same predicate is applied with an
// extra IN (...) clause so an operator-driven targeted retry only
// considers the supplied subset.
func (s *Scanner) embedCandidates(ctx context.Context, req EmbedScanRequest) ([]string, error) {
	resultFP := req.resultFingerprint()
	claimFP := req.claimFingerprint()
	q := `SELECT m.id FROM media m
	 WHERE m.owner_hub = ? AND m.owner_user_id = ?
	   AND m.thumb_status = 'ready'
	   AND (m.hidden_at IS NULL OR ?)
	   AND NOT EXISTS (SELECT 1 FROM media_embedding_ids x
	                    WHERE x.generation_id = ? AND x.media_id = m.id)
	   AND NOT EXISTS (SELECT 1 FROM ai_skipped sk
	                    WHERE sk.media_id = m.id AND sk.task = 'embed')
	   AND NOT EXISTS (SELECT 1 FROM ai_failures f
	                    WHERE f.media_id = m.id AND f.task = 'embed'
	                      AND f.model_id = ? AND f.prompt_version = ''
	                      AND f.input_profile = ?
	                      AND f.attempt_count >= ?)
	   AND NOT EXISTS (SELECT 1 FROM ai_jobs j
	                    WHERE j.media_id = m.id AND j.task = 'embed'
	                      AND j.fingerprint = ?
	                      AND j.status IN ('pending','working','blocked'))`
	args := []any{
		req.Owner.Hub, req.Owner.UserID,
		req.AckAllowsHidden,
		req.Generation.ID,
		resultFP.ModelID, resultFP.InputProfile, req.RetryBudget,
		claimFP,
	}
	if len(req.MediaIDs) > 0 {
		placeholders := strings.Repeat("?,", len(req.MediaIDs))
		placeholders = placeholders[:len(placeholders)-1]
		q += ` AND m.id IN (` + placeholders + `)`
		for _, id := range req.MediaIDs {
			args = append(args, id)
		}
	}
	q += ` ORDER BY m.id LIMIT ?`
	args = append(args, limitOrUnbounded(req.Limit))

	rows, err := s.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// limitOrUnbounded converts the EmbedScanRequest.Limit convention
// (0 = unbounded) into a SQL LIMIT value. SQLite has no LIMIT -1
// shortcut for "all rows" that we trust portably across drivers, so
// we substitute a value larger than any plausible library size.
func limitOrUnbounded(n int) int {
	if n <= 0 {
		return math.MaxInt32
	}
	return n
}

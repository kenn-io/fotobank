// Package jobs is the DB-only layer of the AI pipeline. It owns the
// claim/lease SQL for ai_jobs and the supersession logic. Mirrors the
// shape of internal/thumb/queue.go.
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wesm/fotobank/internal/ai"
)

// ErrClaimLost is returned by Mark* when the lease no longer matches —
// another worker's sweep or a supersession has invalidated this claim.
var ErrClaimLost = errors.New("jobs: claim lost (sweep or supersession)")

// Claim is one row from ClaimBatch.
type Claim struct {
	JobID       string
	MediaID     string
	Fingerprint string
	Attempts    int
	ClaimedAt   time.Time
}

// Counters is per-task aggregates for the AI panel.
type Counters struct {
	Pending int
	Working int
	Blocked int
}

// Queue is safe to share across goroutines.
type Queue struct {
	rw *sql.DB
	ro *sql.DB
}

// NewQueue constructs a Queue. rw must be the writer pool and ro the
// reader pool; depth queries (Counters) read from ro so a slow scrape
// cannot contend with the writer.
func NewQueue(rw, ro *sql.DB) *Queue { return &Queue{rw: rw, ro: ro} }

// Enqueue inserts a pending job for (mediaID, task) under fp.
// Idempotent under the partial unique index when an in-flight job
// already exists for this fingerprint. When an in-flight job exists
// under a different fingerprint, transitions it to 'superseded' and
// inserts the new pending row in one transaction.
func (q *Queue) Enqueue(ctx context.Context, mediaID string, task ai.Task, fp ai.Fingerprint) error {
	return q.EnqueueClaim(ctx, mediaID, task, fp.String())
}

// EnqueueClaim inserts a pending job for (mediaID, task) under the
// claim fingerprint stored in ai_jobs.fingerprint.
func (q *Queue) EnqueueClaim(ctx context.Context, mediaID string, task ai.Task, claimFP string) error {
	tx, err := q.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingID, existingFP string
	row := tx.QueryRowContext(ctx,
		`SELECT id, fingerprint FROM ai_jobs
		 WHERE media_id = ? AND task = ? AND status IN ('pending','working','blocked')
		 LIMIT 1`, mediaID, string(task))
	switch err := row.Scan(&existingID, &existingFP); {
	case errors.Is(err, sql.ErrNoRows):
		// fall through and insert
	case err != nil:
		return fmt.Errorf("lookup existing: %w", err)
	default:
		if existingFP == claimFP {
			return nil // idempotent no-op
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx,
			`UPDATE ai_jobs SET status='superseded', completed_at=?,
			 last_error_kind=?, last_error='fingerprint changed'
			 WHERE id = ?`, now, string(ai.ErrKindSuperseded), existingID); err != nil {
			return fmt.Errorf("supersede old: %w", err)
		}
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ai_jobs(id, media_id, task, fingerprint, status, attempts, enqueued_at)
		 VALUES (?, ?, ?, ?, 'pending', 0, ?)`,
		id, mediaID, string(task), claimFP, now); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return tx.Commit()
}

// SupersedeAll terminally aborts every in-flight job for (media, task).
func (q *Queue) SupersedeAll(ctx context.Context, mediaID string, task ai.Task) error {
	now := time.Now().UTC()
	_, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='superseded', completed_at=?,
		 last_error_kind=?, last_error='fingerprint changed'
		 WHERE media_id=? AND task=? AND status IN ('pending','working','blocked')`,
		now, string(ai.ErrKindSuperseded), mediaID, string(task))
	if err != nil {
		return fmt.Errorf("supersede all: %w", err)
	}
	return nil
}

// SupersedeForFingerprintChange terminally aborts stale in-flight jobs
// for mediaIDs/task whose claim fingerprint differs from newClaimFP.
func (q *Queue) SupersedeForFingerprintChange(ctx context.Context, task ai.Task, mediaIDs []string, newClaimFP string) error {
	if len(mediaIDs) == 0 {
		return nil
	}
	tx, err := q.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	for _, mediaID := range mediaIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE ai_jobs SET status='superseded', completed_at=?,
			 last_error_kind=?, last_error='fingerprint changed'
			 WHERE media_id=? AND task=? AND fingerprint<>?
			   AND status IN ('pending','working','blocked')`,
			now, string(ai.ErrKindSuperseded), mediaID, string(task), newClaimFP); err != nil {
			return fmt.Errorf("supersede %s: %w", mediaID, err)
		}
	}
	return tx.Commit()
}

// ClaimBatch atomically transitions up to n pending rows to 'working'.
func (q *Queue) ClaimBatch(ctx context.Context, task ai.Task, n int) ([]Claim, error) {
	return q.ClaimBatchForFingerprint(ctx, task, "", n)
}

// ClaimBatchForFingerprint atomically transitions up to n pending rows
// for task and claimFP to 'working'. Empty claimFP preserves the legacy
// unfiltered claim behavior.
func (q *Queue) ClaimBatchForFingerprint(ctx context.Context, task ai.Task, claimFP string, n int) ([]Claim, error) {
	if n <= 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	filter := ""
	args := []any{now, string(task)}
	if claimFP != "" {
		filter = " AND fingerprint = ?"
		args = append(args, claimFP)
	}
	args = append(args, n)
	rows, err := q.rw.QueryContext(ctx, `
		UPDATE ai_jobs
		   SET status='working', claimed_at = ?
		 WHERE id IN (
		   SELECT id FROM ai_jobs
		    WHERE task = ? AND status='pending'
		      `+filter+`
		    ORDER BY enqueued_at ASC, id ASC
		    LIMIT ?
		 )
		RETURNING id, media_id, fingerprint, attempts, claimed_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Claim
	for rows.Next() {
		var c Claim
		if err := rows.Scan(&c.JobID, &c.MediaID, &c.Fingerprint, &c.Attempts, &c.ClaimedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim rows: %w", err)
	}
	return out, nil
}

// MarkDone transitions a working row to 'done' if the claim is still ours.
func (q *Queue) MarkDone(ctx context.Context, jobID string, claimedAt time.Time) error {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='done', completed_at=?, last_error=NULL, last_error_kind=NULL
		 WHERE id=? AND status='working' AND claimed_at=?`,
		time.Now().UTC(), jobID, claimedAt)
	if err != nil {
		return fmt.Errorf("mark done: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark done rows affected: %w", err)
	}
	if n == 0 {
		return ErrClaimLost
	}
	return nil
}

// WriteAndMarkDone runs writeFn inside a single transaction and then marks
// the claim done in the same transaction. If the claim has been swept or
// superseded the entire tx is rolled back so writeFn's effects are not
// persisted, and ErrClaimLost is returned. If writeFn returns an error,
// the tx is rolled back and the error is propagated.
func (q *Queue) WriteAndMarkDone(ctx context.Context, c Claim, writeFn func(context.Context, *sql.Tx) error) error {
	tx, err := q.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeFn(ctx, tx); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE ai_jobs SET status='done', completed_at=?, last_error=NULL, last_error_kind=NULL
		 WHERE id=? AND status='working' AND claimed_at=?`,
		time.Now().UTC(), c.JobID, c.ClaimedAt)
	if err != nil {
		return fmt.Errorf("mark done: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark done rows affected: %w", err)
	}
	if n == 0 {
		return ErrClaimLost
	}
	return tx.Commit()
}

// MarkFailed transitions a working row to 'failed' with classification.
// Bumps attempts because failure reflects a real provider/parser run.
func (q *Queue) MarkFailed(ctx context.Context, jobID string, claimedAt time.Time, kind ai.LastErrorKind, errMsg string) error {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='failed', completed_at=?,
		 attempts=attempts+1, last_error=?, last_error_kind=?
		 WHERE id=? AND status='working' AND claimed_at=?`,
		time.Now().UTC(), errMsg, string(kind), jobID, claimedAt)
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark failed rows affected: %w", err)
	}
	if n == 0 {
		return ErrClaimLost
	}
	return nil
}

// MarkRetryable bumps attempts and returns to 'pending' for another try.
func (q *Queue) MarkRetryable(ctx context.Context, jobID string, claimedAt time.Time, kind ai.LastErrorKind, errMsg string) error {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='pending', claimed_at=NULL,
		 attempts=attempts+1, last_error=?, last_error_kind=?
		 WHERE id=? AND status='working' AND claimed_at=?`,
		errMsg, string(kind), jobID, claimedAt)
	if err != nil {
		return fmt.Errorf("mark retryable: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark retryable rows affected: %w", err)
	}
	if n == 0 {
		return ErrClaimLost
	}
	return nil
}

// MarkBlocked marks a working row as blocked (upstream not ready).
// Does not bump attempts; blocked is not a real execution attempt.
func (q *Queue) MarkBlocked(ctx context.Context, jobID string, claimedAt time.Time, reason string) error {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='blocked', claimed_at=NULL,
		 last_error=?, last_error_kind=?
		 WHERE id=? AND status='working' AND claimed_at=?`,
		reason, string(ai.ErrKindThumbBlocked), jobID, claimedAt)
	if err != nil {
		return fmt.Errorf("mark blocked: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark blocked rows affected: %w", err)
	}
	if n == 0 {
		return ErrClaimLost
	}
	return nil
}

// PromoteBlocked transitions a blocked row back to pending.
func (q *Queue) PromoteBlocked(ctx context.Context, jobID string) error {
	_, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='pending', last_error=NULL, last_error_kind=NULL
		 WHERE id=? AND status='blocked'`, jobID)
	if err != nil {
		return fmt.Errorf("promote: %w", err)
	}
	return nil
}

// AckBlockedReason is the canonical last_error text the worker uses when
// parking a job because the owner has not yet acknowledged AI processing.
// Promotion logic keys on this exact value.
const AckBlockedReason = "acknowledgement_required"

// ThumbBlockedPending / Working / Failed are the canonical last_error
// strings the worker uses when parking a job because the source media's
// thumb pipeline is not in 'ready'.
const (
	ThumbBlockedPending = "thumb_pending"
	ThumbBlockedWorking = "thumb_working"
	ThumbBlockedFailed  = "thumb_failed"
)

// PromoteAckedBlocked transitions blocked rows whose last_error indicates
// acknowledgement was missing back to 'pending' if the owning principal
// now has a row in user_settings under settingKey.
func (q *Queue) PromoteAckedBlocked(ctx context.Context, task ai.Task, settingKey string) (int, error) {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs
		    SET status='pending', last_error=NULL, last_error_kind=NULL
		  WHERE task=? AND status='blocked' AND last_error=?
		    AND id IN (
		      SELECT j.id FROM ai_jobs j
		        JOIN media m ON m.id = j.media_id
		        JOIN user_settings s
		          ON s.principal_hub = m.owner_hub
		         AND s.principal_user_id = m.owner_user_id
		         AND s.key = ?
		       WHERE j.task=? AND j.status='blocked' AND j.last_error=?
		    )`,
		string(task), AckBlockedReason, settingKey, string(task), AckBlockedReason)
	if err != nil {
		return 0, fmt.Errorf("promote acked: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("promote acked rows affected: %w", err)
	}
	return int(n), nil
}

// PromoteThumbReadyBlocked transitions blocked rows whose last_error
// indicates the thumbnail pipeline was not ready back to 'pending' if
// the source media's thumb_status is now 'ready'.
func (q *Queue) PromoteThumbReadyBlocked(ctx context.Context, task ai.Task) (int, error) {
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs
		    SET status='pending', last_error=NULL, last_error_kind=NULL
		  WHERE task=? AND status='blocked'
		    AND last_error IN (?, ?, ?)
		    AND media_id IN (SELECT id FROM media WHERE thumb_status='ready')`,
		string(task), ThumbBlockedPending, ThumbBlockedWorking, ThumbBlockedFailed)
	if err != nil {
		return 0, fmt.Errorf("promote thumb-ready: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("promote thumb-ready rows affected: %w", err)
	}
	return int(n), nil
}

// SweepLeases resets working rows whose lease is older than ttl. Does
// not bump attempts: a stuck worker is not a real execution attempt.
func (q *Queue) SweepLeases(ctx context.Context, ttl time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-ttl)
	res, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET status='pending', claimed_at=NULL
		 WHERE status='working' AND claimed_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("sweep: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sweep rows affected: %w", err)
	}
	return int(n), nil
}

// BackdateClaim is a test helper for SweepLeases. Not used in prod.
func (q *Queue) BackdateClaim(ctx context.Context, jobID string, t time.Time) error {
	_, err := q.rw.ExecContext(ctx,
		`UPDATE ai_jobs SET claimed_at=? WHERE id=?`, t, jobID)
	if err != nil {
		return fmt.Errorf("backdate: %w", err)
	}
	return nil
}

// Counters returns the current depth gauges for one task.
func (q *Queue) Counters(ctx context.Context, task ai.Task) (Counters, error) {
	rows, err := q.ro.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM ai_jobs
		 WHERE task=? AND status IN ('pending','working','blocked')
		 GROUP BY status`, string(task))
	if err != nil {
		return Counters{}, fmt.Errorf("counters: %w", err)
	}
	return scanCounters(rows)
}

// CountersByOwner is the owner-scoped variant used by the per-caller
// health surface. Joins through media so other principals' depth is
// not exposed.
func (q *Queue) CountersByOwner(ctx context.Context, task ai.Task, hub, userID string) (Counters, error) {
	rows, err := q.ro.QueryContext(ctx,
		`SELECT j.status, COUNT(*) FROM ai_jobs j
		   JOIN media m ON m.id = j.media_id
		  WHERE j.task=? AND j.status IN ('pending','working','blocked')
		    AND m.owner_hub=? AND m.owner_user_id=?
		  GROUP BY j.status`, string(task), hub, userID)
	if err != nil {
		return Counters{}, fmt.Errorf("counters by owner: %w", err)
	}
	return scanCounters(rows)
}

func scanCounters(rows *sql.Rows) (Counters, error) {
	defer func() { _ = rows.Close() }()
	var c Counters
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return Counters{}, fmt.Errorf("counters scan: %w", err)
		}
		switch status {
		case "pending":
			c.Pending = n
		case "working":
			c.Working = n
		case "blocked":
			c.Blocked = n
		}
	}
	if err := rows.Err(); err != nil {
		return Counters{}, fmt.Errorf("counters rows: %w", err)
	}
	return c, nil
}

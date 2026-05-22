package thumb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
)

// ErrClaimLost is returned by terminal Mark* methods when the
// (id, version, token) WHERE clause matches zero rows — another
// worker's sweep or an Enqueue has superseded our claim.
var ErrClaimLost = errors.New("thumb: claim lost (sweep or regenerate won)")

// Claim is one row handed out by ClaimBatch: the media to process
// plus the claim token (thumb_claimed_at) the worker must pass back
// into Mark* calls. priorityKey is the COALESCE(timestamp,
// imported_at) value used to sort the batch newest-first; it's
// internal to the queue and not exposed on the public Claim type
// (the worker doesn't need it).
type Claim struct {
	Media     media.Media
	ClaimedAt time.Time

	priorityKey time.Time
}

// EnqueueFilter narrows which rows Enqueue targets. Either All or at
// least one of IDs/MediaType/Status/Since must be set. Owner scopes
// the filter to one principal.
type EnqueueFilter struct {
	Owner     owners.Principal
	All       bool
	IDs       []string
	MediaType media.Type
	Status    string
	Since     *time.Time
}

// Queue is the DB-only layer of the thumbnail pipeline. It owns the
// claim/lease SQL and is safe to share across goroutines.
type Queue struct {
	rw *sql.DB
	ro *sql.DB
}

// NewQueue constructs a Queue. rw must be the writer pool and ro the
// reader pool; only rw is used today but the reader is accepted now so
// future read-heavy methods (e.g. pending counts) can use it without
// changing the constructor.
func NewQueue(rw, ro *sql.DB) *Queue {
	return &Queue{rw: rw, ro: ro}
}

// claimBatchSQL drains pending rows newest-first so the photos a user
// will actually look at right after import (the latest ones, which
// land at the top of /library) get thumbs before the long tail of
// older imports. We sort by the photo's EXIF-derived `timestamp`
// when present, falling back to `imported_at` for rows missing EXIF
// dates, then by `id` for a deterministic tiebreaker. The previous
// FIFO ordering by imported_at meant a user importing today's shoot
// after a 200-frame archive backfill would see the newest day's
// thumbs last — which manifested as a "broken" library on first
// open even though the worker was making steady progress.
const claimBatchSQL = `
UPDATE media
   SET thumb_status     = 'working',
       thumb_claimed_at = ?
 WHERE id IN (
     SELECT id FROM media
      WHERE thumb_status = 'pending'
      ORDER BY COALESCE(timestamp, imported_at) DESC, id ASC
      LIMIT ?
 )
RETURNING id, owner_hub, owner_user_id, media_type, mime_type, path,
          thumb_version, checksum, thumb_claimed_at,
          COALESCE(timestamp, imported_at) AS priority_key
`

// ClaimBatch transitions up to n pending rows to 'working' and returns
// the claimed rows with their claim tokens. Uses a single UPDATE…
// RETURNING so the claim is atomic under SQLite's write lock.
func (q *Queue) ClaimBatch(ctx context.Context, n int) ([]Claim, error) {
	if n <= 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	rows, err := q.rw.QueryContext(ctx, claimBatchSQL, now, n)
	if err != nil {
		return nil, fmt.Errorf("claim batch: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Claim
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim rows: %w", err)
	}
	// SQLite's UPDATE...RETURNING does NOT preserve the inner
	// SELECT's ORDER BY — the rows we wanted (newest-first) come
	// back in some implementation-defined order. Sort the slice
	// here so the worker dispatches them in priority order.
	// Stable sort with priorityKey desc, then id asc as tiebreak.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].priorityKey.Equal(out[j].priorityKey) {
			return out[i].priorityKey.After(out[j].priorityKey)
		}
		return out[i].Media.ID < out[j].Media.ID
	})
	return out, nil
}

func scanClaim(rows *sql.Rows) (Claim, error) {
	var (
		c              Claim
		mediaType      string
		claimedAt      time.Time
		priorityKeyRaw string
	)
	if err := rows.Scan(
		&c.Media.ID,
		&c.Media.Owner.Hub,
		&c.Media.Owner.UserID,
		&mediaType,
		&c.Media.MimeType,
		&c.Media.Path,
		&c.Media.ThumbVersion,
		&c.Media.Checksum,
		&claimedAt,
		&priorityKeyRaw,
	); err != nil {
		return Claim{}, fmt.Errorf("scan claim: %w", err)
	}
	c.Media.Type = media.Type(mediaType)
	c.ClaimedAt = claimedAt
	// COALESCE through sqlite returns a TEXT, not a typed timestamp,
	// so the mattn driver's automatic time.Time materialization
	// doesn't kick in. Parse the RFC3339 form the schema writes.
	// A bad parse falls back to imported_at — never zero, never
	// nil — so SortStable below still has a valid ordering key.
	if t, err := time.Parse(time.RFC3339Nano, priorityKeyRaw); err == nil {
		c.priorityKey = t
	} else if t, err := time.Parse(time.RFC3339, priorityKeyRaw); err == nil {
		c.priorityKey = t
	}
	return c, nil
}

const sweepLeasesSQL = `
UPDATE media
   SET thumb_status     = 'pending',
       thumb_claimed_at = NULL,
       thumb_version    = thumb_version + 1,
       thumb_updated_at = ?
 WHERE thumb_status = 'working'
   AND thumb_claimed_at < ?
`

// SweepLeases returns 'working' rows whose lease has expired (older
// than `after`) back to 'pending', bumping thumb_version so lease
// retries never collide with the no-clobber storage write. Returns
// the number of rows reset.
func (q *Queue) SweepLeases(ctx context.Context, after time.Duration) (int, error) {
	now := time.Now().UTC()
	cutoff := now.Add(-after)
	res, err := q.rw.ExecContext(ctx, sweepLeasesSQL, now, cutoff)
	if err != nil {
		return 0, fmt.Errorf("sweep leases: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sweep rows affected: %w", err)
	}
	return int(n), nil
}

const markReadySQL = `
UPDATE media
   SET thumb_status     = 'ready',
       thumb_updated_at = ?,
       thumb_claimed_at = NULL
 WHERE id = ? AND thumb_version = ? AND thumb_claimed_at = ?
`

// MarkReady transitions a claimed row to 'ready'. Returns ErrClaimLost
// if the (id, version, token) triple no longer matches — typically
// because a sweep bumped the version or an Enqueue re-queued the row.
//
// Equivalent to MarkReadyWithHook(ctx, id, version, token, nil) — the
// hookless form remains for callers (notably queue tests) that don't
// need the embed-mapping invalidation that lands inside MarkReadyWithHook.
func (q *Queue) MarkReady(ctx context.Context, id string, version int, token time.Time) error {
	return q.MarkReadyWithHook(ctx, id, version, token, nil)
}

// MarkReadyHook runs inside the MarkReady write transaction after the
// row UPDATE has applied (and only when the UPDATE actually transitioned
// the row — i.e. the claim fence held). Returning a non-nil error rolls
// back the entire MarkReady, so the row stays 'working' and a subsequent
// sweep will re-queue it.
//
// Used by the worker to attach side effects that must observe the same
// "this regen actually finalised" guarantee as the status transition —
// notably embedding.OnThumbRegen, which invalidates stale vec mappings
// across non-retired generations.
type MarkReadyHook func(ctx context.Context, tx *sql.Tx) error

// MarkReadyWithHook is the transaction-bound MarkReady: the UPDATE and
// hook execute inside one write tx so a hook error rolls back the row
// transition. ErrClaimLost is returned (with no hook invocation) when
// the (id, version, token) fence misses, matching MarkReady's contract.
//
// hook may be nil, in which case behaviour is identical to MarkReady's
// single-statement variant — same atomicity guarantees, same error
// surface.
func (q *Queue) MarkReadyWithHook(
	ctx context.Context,
	id string,
	version int,
	token time.Time,
	hook MarkReadyHook,
) error {
	tx, err := q.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mark ready %s begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, markReadySQL, time.Now().UTC(), id, version, token)
	if err != nil {
		return fmt.Errorf("mark ready %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark ready %s rows affected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s version=%d", ErrClaimLost, id, version)
	}
	if hook != nil {
		if err := hook(ctx, tx); err != nil {
			return fmt.Errorf("mark ready %s hook: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mark ready %s commit: %w", id, err)
	}
	return nil
}

const markNoPreviewSQL = `
UPDATE media
   SET thumb_status     = 'no_preview',
       thumb_updated_at = ?,
       thumb_claimed_at = NULL
 WHERE id = ? AND thumb_version = ? AND thumb_claimed_at = ?
`

// MarkNoPreview transitions a claimed row to 'no_preview' — the source
// format is one we intentionally do not decode (e.g. HEIC without
// CGO). Same fence semantics as MarkReady.
func (q *Queue) MarkNoPreview(ctx context.Context, id string, version int, token time.Time) error {
	return q.finalize(ctx, markNoPreviewSQL, id, version, token)
}

const markFailedSQL = `
UPDATE media
   SET thumb_status     = 'failed',
       thumb_updated_at = ?,
       thumb_claimed_at = NULL
 WHERE id = ? AND thumb_version = ? AND thumb_claimed_at = ?
`

// MarkFailed transitions a claimed row to 'failed'. The trailing error
// argument is accepted so callers can pass the decode/encode error
// verbatim; it is not persisted today but reserved for future use
// (e.g. a thumb_error column). Same fence semantics as MarkReady.
func (q *Queue) MarkFailed(
	ctx context.Context,
	id string,
	version int,
	token time.Time,
	_ error,
) error {
	return q.finalize(ctx, markFailedSQL, id, version, token)
}

func (q *Queue) finalize(
	ctx context.Context,
	query, id string,
	version int,
	token time.Time,
) error {
	res, err := q.rw.ExecContext(ctx, query, time.Now().UTC(), id, version, token)
	if err != nil {
		return fmt.Errorf("finalize %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finalize %s rows affected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s version=%d", ErrClaimLost, id, version)
	}
	return nil
}

// DepthByState returns the count of rows in thumb_status = state.
// Used as a closure source for the obs.Metrics ThumbQueueDepth gauge,
// which T13 wires via MetricSources at server boot. Reads from the
// reader pool so a slow scrape cannot contend with the writer.
func (q *Queue) DepthByState(ctx context.Context, state string) (int64, error) {
	var n int64
	err := q.ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media WHERE thumb_status = ?`, state).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("thumb queue depth %q: %w", state, err)
	}
	return n, nil
}

// Enqueue bumps thumb_version and sets thumb_status='pending' on every
// row matching filter. Returns the number of rows updated.
func (q *Queue) Enqueue(ctx context.Context, filter EnqueueFilter) (int, error) {
	where, args, err := buildEnqueueWhere(filter)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	query := `UPDATE media
		  SET thumb_status     = 'pending',
		      thumb_version    = thumb_version + 1,
		      thumb_updated_at = ?,
		      thumb_claimed_at = NULL
		   WHERE ` + where
	execArgs := append([]any{now}, args...)
	res, err := q.rw.ExecContext(ctx, query, execArgs...)
	if err != nil {
		return 0, fmt.Errorf("enqueue: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("enqueue rows affected: %w", err)
	}
	return int(n), nil
}

func buildEnqueueWhere(filter EnqueueFilter) (string, []any, error) {
	if (filter.Owner == owners.Principal{}) {
		return "", nil, errors.New("thumb: Enqueue requires Owner")
	}
	parts := []string{"owner_hub = ?", "owner_user_id = ?"}
	args := []any{filter.Owner.Hub, filter.Owner.UserID}
	specific := filter.All
	if len(filter.IDs) > 0 {
		placeholders := strings.Repeat("?,", len(filter.IDs))
		placeholders = placeholders[:len(placeholders)-1]
		parts = append(parts, "id IN ("+placeholders+")")
		for _, id := range filter.IDs {
			args = append(args, id)
		}
		specific = true
	}
	if filter.MediaType != "" {
		parts = append(parts, "media_type = ?")
		args = append(args, string(filter.MediaType))
		specific = true
	}
	if filter.Status != "" {
		parts = append(parts, "thumb_status = ?")
		args = append(args, filter.Status)
		specific = true
	}
	if filter.Since != nil {
		parts = append(parts, "imported_at >= ?")
		args = append(args, *filter.Since)
		specific = true
	}
	if !specific {
		return "", nil, errors.New("thumb: Enqueue requires All or at least one selector")
	}
	return strings.Join(parts, " AND "), args, nil
}

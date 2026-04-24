package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
)

// Repo is a SQLite-backed store of scopes + scope_media rows. Split
// read/write pool: writes go through rw, reads through ro.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool, ro the reader.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// scopeColumns lists all scope columns in a canonical order shared by
// every SELECT in this file and by scanScope.
const scopeColumns = `
    uuid, owner_hub, owner_user_id, grantee_hub, grantee_user_id,
    target_type, target_album_id, allow_download, label,
    created_at, expires_at, revoked_at,
    broker_status, broker_registered_at, broker_granted_at,
    broker_revoked_at, broker_last_error, broker_attempts,
    broker_next_attempt_at
`

// Insert writes the scopes row and (when target_type = media_set) its
// scope_media rows in one transaction. Owner-consistency triggers on
// scope_media and scopes.target_album_id are last-line defence; the
// service layer pre-flights ownership.
func (r *Repo) Insert(ctx context.Context, s Scope, mediaIDs []string) error {
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
        INSERT INTO scopes (
            uuid, owner_hub, owner_user_id, grantee_hub, grantee_user_id,
            target_type, target_album_id, allow_download, label,
            created_at, expires_at,
            broker_status, broker_attempts
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		s.UUID, s.Owner.Hub, s.Owner.UserID, s.Grantee.Hub, s.Grantee.UserID,
		string(s.TargetType), s.TargetAlbumID, boolToInt(s.AllowDownload), nullString(s.Label),
		s.CreatedAt, nullTime(s.ExpiresAt),
		string(s.BrokerStatus),
	)
	if err != nil {
		return fmt.Errorf("insert scope: %w", err)
	}
	if s.TargetType == TargetMediaSet {
		stmt, perr := tx.PrepareContext(ctx,
			`INSERT INTO scope_media (scope_uuid, media_id) VALUES (?, ?)`)
		if perr != nil {
			return fmt.Errorf("prepare scope_media: %w", perr)
		}
		defer func() { _ = stmt.Close() }()
		for _, mid := range mediaIDs {
			if _, err := stmt.ExecContext(ctx, s.UUID, mid); err != nil {
				return fmt.Errorf("insert scope_media: %w", err)
			}
		}
	}
	return tx.Commit()
}

// GetByUUID returns the scope plus (for media_set) its membership.
// Returns errs.ErrNotFound when no row exists.
func (r *Repo) GetByUUID(ctx context.Context, uuidStr string) (ScopeDetail, error) {
	row := r.ro.QueryRowContext(ctx,
		`SELECT `+scopeColumns+` FROM scopes WHERE uuid = ?`, uuidStr)
	s, err := scanScope(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ScopeDetail{}, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, uuidStr)
	}
	if err != nil {
		return ScopeDetail{}, fmt.Errorf("get scope: %w", err)
	}
	det := ScopeDetail{Scope: s}
	if s.TargetType == TargetMediaSet {
		rows, qerr := r.ro.QueryContext(ctx,
			`SELECT media_id FROM scope_media WHERE scope_uuid = ? ORDER BY media_id`,
			uuidStr)
		if qerr != nil {
			return ScopeDetail{}, fmt.Errorf("list scope_media: %w", qerr)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var mid string
			if err := rows.Scan(&mid); err != nil {
				return ScopeDetail{}, fmt.Errorf("scan scope_media: %w", err)
			}
			det.MediaIDs = append(det.MediaIDs, mid)
		}
		if err := rows.Err(); err != nil {
			return ScopeDetail{}, fmt.Errorf("iter scope_media: %w", err)
		}
	}
	return det, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanScope(s rowScanner) (Scope, error) {
	var (
		sc            Scope
		targetAlbumID sql.NullString
		allowDownload int64
		label         sql.NullString
		expiresAt     sql.NullTime
		revokedAt     sql.NullTime
		brokerRegAt   sql.NullTime
		brokerGrAt    sql.NullTime
		brokerRevAt   sql.NullTime
		brokerLastErr sql.NullString
		brokerNextAt  sql.NullTime
		targetType    string
		brokerStatus  string
	)
	if err := s.Scan(
		&sc.UUID, &sc.Owner.Hub, &sc.Owner.UserID,
		&sc.Grantee.Hub, &sc.Grantee.UserID,
		&targetType, &targetAlbumID, &allowDownload, &label,
		&sc.CreatedAt, &expiresAt, &revokedAt,
		&brokerStatus, &brokerRegAt, &brokerGrAt,
		&brokerRevAt, &brokerLastErr, &sc.BrokerAttempts,
		&brokerNextAt,
	); err != nil {
		return Scope{}, err
	}
	sc.TargetType = TargetType(targetType)
	sc.BrokerStatus = BrokerStatus(brokerStatus)
	if targetAlbumID.Valid {
		v := targetAlbumID.String
		sc.TargetAlbumID = &v
	}
	sc.AllowDownload = allowDownload != 0
	if label.Valid {
		sc.Label = label.String
	}
	if expiresAt.Valid {
		v := expiresAt.Time
		sc.ExpiresAt = &v
	}
	if revokedAt.Valid {
		v := revokedAt.Time
		sc.RevokedAt = &v
	}
	if brokerRegAt.Valid {
		v := brokerRegAt.Time
		sc.BrokerRegisteredAt = &v
	}
	if brokerGrAt.Valid {
		v := brokerGrAt.Time
		sc.BrokerGrantedAt = &v
	}
	if brokerRevAt.Valid {
		v := brokerRevAt.Time
		sc.BrokerRevokedAt = &v
	}
	if brokerLastErr.Valid {
		sc.BrokerLastError = brokerLastErr.String
	}
	if brokerNextAt.Valid {
		v := brokerNextAt.Time
		sc.BrokerNextAttemptAt = &v
	}
	return sc, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

// ListByOwner returns scopes owned by this principal, filtered and
// paginated. When len(filter.Status) == 0 and filter.IncludeSettled
// is false, broker_status = 'revoked_remote' is hidden by default;
// every other row is visible. When len(filter.Status) > 0, only those
// statuses are included and IncludeSettled is ignored. AlbumID /
// Grantee further narrow the result when non-zero.
//
// Offset is ignored when Limit is zero — callers must pass a non-zero
// Limit to paginate.
func (r *Repo) ListByOwner(ctx context.Context, owner owners.Principal, filter ScopeFilter) ([]Scope, error) {
	var (
		sb   strings.Builder
		args []any
	)
	sb.WriteString(`SELECT ` + scopeColumns + ` FROM scopes WHERE owner_hub = ? AND owner_user_id = ?`)
	args = append(args, owner.Hub, owner.UserID)

	switch {
	case len(filter.Status) > 0:
		ph, sargs := statusPlaceholders(filter.Status)
		sb.WriteString(` AND broker_status IN (` + ph + `)`)
		args = append(args, sargs...)
	case !filter.IncludeSettled:
		sb.WriteString(` AND broker_status != 'revoked_remote'`)
	}
	if filter.AlbumID != "" {
		sb.WriteString(` AND target_album_id = ?`)
		args = append(args, filter.AlbumID)
	}
	if !filter.Grantee.IsZero() {
		sb.WriteString(` AND grantee_hub = ? AND grantee_user_id = ?`)
		args = append(args, filter.Grantee.Hub, filter.Grantee.UserID)
	}
	sb.WriteString(` ORDER BY created_at DESC, uuid ASC`)
	if filter.Limit > 0 {
		sb.WriteString(` LIMIT ?`)
		args = append(args, filter.Limit)
		if filter.Offset > 0 {
			sb.WriteString(` OFFSET ?`)
			args = append(args, filter.Offset)
		}
	}

	rows, err := r.ro.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Scope
	for rows.Next() {
		s, err := scanScope(rows)
		if err != nil {
			return nil, fmt.Errorf("scan scope: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListReady returns up to limit scopes the outbox worker should attempt
// right now: status in (pending, revoking), broker_next_attempt_at is
// either NULL or <= now, and broker_attempts < MaxBrokerAttempts.
// Ordered by broker_next_attempt_at ASC NULLS FIRST, then created_at
// ASC, so newly-inserted rows are picked up promptly.
func (r *Repo) ListReady(ctx context.Context, now time.Time, limit int) ([]Scope, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.ro.QueryContext(ctx,
		`SELECT `+scopeColumns+` FROM scopes
          WHERE broker_status IN ('pending', 'revoking')
            AND (broker_next_attempt_at IS NULL OR broker_next_attempt_at <= ?)
            AND broker_attempts < ?
          ORDER BY (broker_next_attempt_at IS NULL) DESC,
                   broker_next_attempt_at ASC,
                   created_at ASC
          LIMIT ?`,
		now, MaxBrokerAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("list ready scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Scope
	for rows.Next() {
		s, err := scanScope(rows)
		if err != nil {
			return nil, fmt.Errorf("scan scope: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkPublished records a successful PublishScope. Timestamps land
// whether the row is still 'pending' or has already moved to
// 'revoking' (owner-Revoke raced), so the owner-facing UI shows
// "was granted" honestly. Only the status transition to 'active'
// is fenced to broker_status = 'pending'; if the row is already
// 'revoking', status stays 'revoking' and the worker issues
// RevokeScope on the next tick.
//
// Rows-affected = 1 does NOT mean broker_status is now 'active';
// it may still be 'revoking'. Callers that care must re-read.
func (r *Repo) MarkPublished(ctx context.Context, uuidStr string, at time.Time) (int64, error) {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE scopes
            SET broker_registered_at = COALESCE(broker_registered_at, ?),
                broker_granted_at    = COALESCE(broker_granted_at, ?),
                broker_status = CASE WHEN broker_status = 'pending' THEN 'active' ELSE broker_status END,
                broker_last_error = CASE WHEN broker_status = 'pending' THEN '' ELSE broker_last_error END,
                broker_next_attempt_at = CASE WHEN broker_status = 'pending' THEN NULL ELSE broker_next_attempt_at END
          WHERE uuid = ? AND broker_status IN ('pending', 'revoking')`,
		at, at, uuidStr)
	if err != nil {
		return 0, fmt.Errorf("mark published: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark published rows affected: %w", err)
	}
	return n, nil
}

// MarkAttemptFailed records a retryable broker failure. Bumps
// broker_attempts, stores err, and schedules the next attempt. Fenced
// to the caller-declared phase (pending or revoking) so the row
// cannot drift into the wrong state if owner Revoke raced between
// ListReady and this UPDATE.
func (r *Repo) MarkAttemptFailed(ctx context.Context, uuidStr string, phase BrokerStatus, errMsg string, nextAt time.Time) (int64, error) {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE scopes
            SET broker_attempts = broker_attempts + 1,
                broker_last_error = ?,
                broker_next_attempt_at = ?
          WHERE uuid = ? AND broker_status = ?`,
		errMsg, nextAt, uuidStr, string(phase))
	if err != nil {
		return 0, fmt.Errorf("mark attempt failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark attempt failed rows affected: %w", err)
	}
	return n, nil
}

// MarkFailed flips a scope to broker_status = 'failed' after exceeding
// MaxBrokerAttempts or on a permanent error. Fenced to the caller-
// declared phase. broker_attempts is bumped one more time (so the row
// records that the final attempt happened).
func (r *Repo) MarkFailed(ctx context.Context, uuidStr string, phase BrokerStatus, errMsg string) (int64, error) {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE scopes
            SET broker_status = 'failed',
                broker_attempts = broker_attempts + 1,
                broker_last_error = ?,
                broker_next_attempt_at = NULL
          WHERE uuid = ? AND broker_status = ?`,
		errMsg, uuidStr, string(phase))
	if err != nil {
		return 0, fmt.Errorf("mark failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark failed rows affected: %w", err)
	}
	return n, nil
}

// SetRevoking is the service-level entry point for Revoke. It marks
// the scope as revoked locally and queues the broker revocation. The
// caller-declared "at" is written only if revoked_at is currently
// NULL, so a second revoke is a no-op instead of clobbering the
// original revoke timestamp. Fenced to states that are still
// owner-actionable.
//
// Returns rows-affected. 0 means the row was already revoking,
// revoked_remote, or does not exist; the service layer (T14) is
// expected to translate that into ErrScopeAlreadyRevoked.
func (r *Repo) SetRevoking(ctx context.Context, uuidStr string, at time.Time) (int64, error) {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE scopes
            SET broker_status = 'revoking',
                revoked_at = COALESCE(revoked_at, ?),
                broker_attempts = 0,
                broker_next_attempt_at = NULL,
                broker_last_error = ''
          WHERE uuid = ?
            AND broker_status IN ('pending', 'active', 'failed')
            AND revoked_at IS NULL`,
		at, uuidStr)
	if err != nil {
		return 0, fmt.Errorf("set revoking: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("set revoking rows affected: %w", err)
	}
	return n, nil
}

// MarkRevoked transitions a revoking scope to revoked_remote after a
// successful RevokeScope call. Fenced to broker_status = 'revoking'
// AND revoked_at IS NOT NULL — both invariants are established by
// SetRevoking, so a row that satisfies the status fence without a
// revoked_at is a bug upstream; we refuse to transition rather than
// silently producing a revoked_remote row with no local revoke
// timestamp.
func (r *Repo) MarkRevoked(ctx context.Context, uuidStr string, at time.Time) (int64, error) {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE scopes
            SET broker_status = 'revoked_remote',
                broker_revoked_at = COALESCE(broker_revoked_at, ?),
                broker_last_error = '',
                broker_next_attempt_at = NULL
          WHERE uuid = ?
            AND broker_status = 'revoking'
            AND revoked_at IS NOT NULL`,
		at, uuidStr)
	if err != nil {
		return 0, fmt.Errorf("mark revoked: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark revoked rows affected: %w", err)
	}
	return n, nil
}

// RetryPublish moves a failed scope back to pending so the worker
// retries PublishScope. Applies only to failed rows whose revoked_at
// is NULL (publish-side stall). Returns rows-affected.
func (r *Repo) RetryPublish(ctx context.Context, uuidStr string) (int64, error) {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE scopes
            SET broker_status = 'pending',
                broker_attempts = 0,
                broker_next_attempt_at = NULL,
                broker_last_error = ''
          WHERE uuid = ? AND broker_status = 'failed' AND revoked_at IS NULL`,
		uuidStr)
	if err != nil {
		return 0, fmt.Errorf("retry publish: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retry publish rows affected: %w", err)
	}
	return n, nil
}

// RetryRevoke moves a failed scope back to revoking so the worker
// retries RevokeScope. Applies only to failed rows whose revoked_at
// is NOT NULL (revoke-side stall). Returns rows-affected.
func (r *Repo) RetryRevoke(ctx context.Context, uuidStr string) (int64, error) {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE scopes
            SET broker_status = 'revoking',
                broker_attempts = 0,
                broker_next_attempt_at = NULL,
                broker_last_error = ''
          WHERE uuid = ? AND broker_status = 'failed' AND revoked_at IS NOT NULL`,
		uuidStr)
	if err != nil {
		return 0, fmt.Errorf("retry revoke: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retry revoke rows affected: %w", err)
	}
	return n, nil
}

// PrepareAlbumDeleteTx runs inside an album-delete transaction (opened
// by AlbumService on the rw pool). It purges safely-terminal scopes
// linked to the album (broker_status = 'revoked_remote' only, because
// a worker crash between PublishScope success and MarkPublished means
// no other status can be proved safe) and returns
// ErrAlbumHasLiveScopes if any row remains that is not
// revoked_remote. See the spec's §8.3 for the crash-window rationale.
// Caller commits the tx (purging on success) or rolls it back (no
// changes on block).
func (r *Repo) PrepareAlbumDeleteTx(ctx context.Context, tx *sql.Tx, albumID string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM scopes
          WHERE target_album_id = ? AND broker_status = 'revoked_remote'`,
		albumID); err != nil {
		return fmt.Errorf("purge revoked_remote scopes: %w", err)
	}
	row := tx.QueryRowContext(ctx,
		`SELECT 1 FROM scopes
          WHERE target_album_id = ? AND broker_status != 'revoked_remote'
          LIMIT 1`, albumID)
	var one int
	switch err := row.Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("check blocking scopes: %w", err)
	default:
		return ErrAlbumHasLiveScopes
	}
}

// HasBlockingScopesForAlbum is the non-tx diagnostic helper. Reads
// through the ro pool; small races against a concurrent state
// transition are acceptable because this is a preview, not the
// authoritative album-delete decision.
func (r *Repo) HasBlockingScopesForAlbum(ctx context.Context, albumID string) (bool, error) {
	row := r.ro.QueryRowContext(ctx,
		`SELECT 1 FROM scopes
          WHERE target_album_id = ? AND broker_status != 'revoked_remote'
          LIMIT 1`, albumID)
	var one int
	switch err := row.Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("has blocking scopes: %w", err)
	default:
		return true, nil
	}
}

// ValidateHeaderScopes returns the live, grantee-matching subset of
// uuids. Live means revoked_at IS NULL AND broker_status = 'active' AND
// (expires_at IS NULL OR expires_at > now). Order of returned rows is
// unspecified; callers who need a stable order sort themselves. Empty
// uuids returns (nil, nil) without a query.
func (r *Repo) ValidateHeaderScopes(
	ctx context.Context,
	caller owners.Principal,
	uuids []string,
	now time.Time,
) ([]Scope, error) {
	if len(uuids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(uuids))
	placeholders = placeholders[:len(placeholders)-1]
	q := `SELECT ` + scopeColumns + `
  FROM scopes
 WHERE uuid IN (` + placeholders + `)
   AND grantee_hub = ?
   AND grantee_user_id = ?
   AND revoked_at IS NULL
   AND broker_status = 'active'
   AND (expires_at IS NULL OR expires_at > ?)`
	args := make([]any, 0, len(uuids)+3)
	for _, u := range uuids {
		args = append(args, u)
	}
	args = append(args, caller.Hub, caller.UserID, now)

	rows, err := r.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("validate header scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Scope
	for rows.Next() {
		s, err := scanScope(rows)
		if err != nil {
			return nil, fmt.Errorf("scan validated scope: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CoverMediaByScopes runs pass 2 of CheckMediaAccess per spec §6.3:
// given a retained-owner validated slice, returns one AccessPath per
// covering scope. The retained-owner predicate on scopes.owner_hub /
// owner_user_id is a belt-and-braces guard so a bug in the Go-side
// degradation cannot leak a dropped-owner scope through the DB layer.
func (r *Repo) CoverMediaByScopes(
	ctx context.Context,
	validated []Scope,
	owner owners.Principal,
	mediaID string,
) (AccessDecision, error) {
	if len(validated) == 0 {
		return AccessDecision{}, nil
	}
	valRows := make([]string, 0, len(validated))
	args := make([]any, 0, len(validated)*4+4)
	for _, s := range validated {
		valRows = append(valRows, "(?, ?, ?, ?)")
		var albumID any
		if s.TargetAlbumID != nil {
			albumID = *s.TargetAlbumID
		}
		args = append(args, s.UUID, string(s.TargetType), albumID, boolToInt(s.AllowDownload))
	}
	args = append(args, owner.Hub, owner.UserID, mediaID, mediaID)

	q := `
WITH validated(uuid, target_type, target_album_id, allow_download) AS (
    VALUES ` + strings.Join(valRows, ",") + `
)
SELECT v.uuid, v.target_type, v.target_album_id, v.allow_download
  FROM validated v
  JOIN scopes s ON s.uuid = v.uuid
 WHERE s.owner_hub = ? AND s.owner_user_id = ?
   AND (
         (v.target_type = 'media_set' AND EXISTS (
             SELECT 1 FROM scope_media sm
              WHERE sm.scope_uuid = v.uuid AND sm.media_id = ?
         ))
      OR (v.target_type = 'album_live' AND EXISTS (
             SELECT 1 FROM album_media am
              WHERE am.album_id = v.target_album_id AND am.media_id = ?
         ))
       )
`
	rows, err := r.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return AccessDecision{}, fmt.Errorf("cover media by scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	paths := make([]AccessPath, 0, len(validated))
	for rows.Next() {
		var (
			p         AccessPath
			albumID   sql.NullString
			allowInt  int
			targetStr string
		)
		if err := rows.Scan(&p.ScopeUUID, &targetStr, &albumID, &allowInt); err != nil {
			return AccessDecision{}, fmt.Errorf("scan cover row: %w", err)
		}
		if albumID.Valid {
			a := albumID.String
			p.AlbumID = &a
		}
		p.AllowDownload = allowInt != 0
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		return AccessDecision{}, fmt.Errorf("iter cover rows: %w", err)
	}
	return AccessDecision{Authorized: len(paths) > 0, Paths: paths}, nil
}

// CoverAlbumByScopes runs the album_live-only pass 2 of
// CheckAlbumAccess. Media_set scopes are silently ignored even if
// their members belong to albumID; only an album_live scope targeting
// this album authorises. The retained-owner predicate on
// scopes.owner_hub / owner_user_id is a belt-and-braces guard so a
// bug in the Go-side degradation cannot leak a dropped-owner scope
// through the DB layer.
func (r *Repo) CoverAlbumByScopes(
	ctx context.Context,
	validated []Scope,
	owner owners.Principal,
	albumID string,
) (AccessDecision, error) {
	if len(validated) == 0 {
		return AccessDecision{}, nil
	}
	// Filter to album_live entries up front so the SQL has no branch.
	// Copy into a fresh slice rather than reusing validated's backing
	// array: callers may still hold references to the original slice.
	live := make([]Scope, 0, len(validated))
	for _, s := range validated {
		if s.TargetType == TargetAlbumLive && s.TargetAlbumID != nil {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return AccessDecision{}, nil
	}
	valRows := make([]string, 0, len(live))
	args := make([]any, 0, len(live)*3+3)
	for _, s := range live {
		valRows = append(valRows, "(?, ?, ?)")
		args = append(args, s.UUID, *s.TargetAlbumID, boolToInt(s.AllowDownload))
	}
	args = append(args, owner.Hub, owner.UserID, albumID)

	q := `
WITH validated(uuid, target_album_id, allow_download) AS (
    VALUES ` + strings.Join(valRows, ",") + `
)
SELECT v.uuid, v.target_album_id, v.allow_download
  FROM validated v
  JOIN scopes s ON s.uuid = v.uuid
 WHERE s.owner_hub = ? AND s.owner_user_id = ?
   AND v.target_album_id = ?
`
	rows, err := r.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return AccessDecision{}, fmt.Errorf("cover album by scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	paths := make([]AccessPath, 0, len(live))
	for rows.Next() {
		var (
			p        AccessPath
			albumStr string
			allowInt int
		)
		if err := rows.Scan(&p.ScopeUUID, &albumStr, &allowInt); err != nil {
			return AccessDecision{}, fmt.Errorf("scan cover album row: %w", err)
		}
		a := albumStr
		p.AlbumID = &a
		p.AllowDownload = allowInt != 0
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		return AccessDecision{}, fmt.Errorf("iter cover album rows: %w", err)
	}
	return AccessDecision{Authorized: len(paths) > 0, Paths: paths}, nil
}

// statusPlaceholders renders `IN (?,?,?)` argument tuples. Returns the
// placeholder string and the []any args, both empty when the input is
// empty. Used here by ListByOwner and in later tasks for filtered
// UPDATEs.
func statusPlaceholders(statuses []BrokerStatus) (string, []any) {
	if len(statuses) == 0 {
		return "", nil
	}
	parts := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, s := range statuses {
		parts[i] = "?"
		args[i] = string(s)
	}
	return strings.Join(parts, ","), args
}

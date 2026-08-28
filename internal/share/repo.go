package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
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
		// Filter against media.hidden_at so a member that became hidden
		// after scope creation is not exposed to the grantee.
		rows, qerr := r.ro.QueryContext(ctx,
			`SELECT sm.media_id FROM scope_media sm
			   JOIN media m ON m.id = sm.media_id
			  WHERE sm.scope_uuid = ? AND m.hidden_at IS NULL
			  ORDER BY sm.media_id`,
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
	// allow_download is declared BOOLEAN in the schema. Under
	// mattn/go-sqlite3, BOOLEAN-affinity columns Scan into Go bool
	// natively (the prior modernc driver returned int64). Scanning
	// directly into sc.AllowDownload — a bool field — round-trips
	// without an intermediate int.
	if err := s.Scan(
		&sc.UUID, &sc.Owner.Hub, &sc.Owner.UserID,
		&sc.Grantee.Hub, &sc.Grantee.UserID,
		&targetType, &targetAlbumID, &sc.AllowDownload, &label,
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

// CoverMediaByScopes runs the coverage-query pass of CheckMediaAccess.
// Given a retained-owner validated slice, it returns one AccessPath per
// covering scope. The retained-owner predicate on scopes.owner_hub /
// owner_user_id is a belt-and-braces guard so a bug in the Go-side
// degradation cannot leak a dropped-owner scope through the DB layer.
//
// Sidecar transitive coverage (F2.2 §8.2): a request for a sidecar's
// id is also authorised when the sidecar's paired primary is itself
// covered. The query expresses this by matching scope_media.media_id /
// album_media.media_id against either the requested mediaID directly
// or its primary (looked up via media.paired_with_id). Listing-side
// queries (ListSharedMediaIDs) deliberately keep their primary-only
// behavior — sidecars are downloadable attachments, not grid rows.
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
	args := make([]any, 0, len(validated)*4+7)
	for _, s := range validated {
		valRows = append(valRows, "(?, ?, ?, ?)")
		var albumID any
		if s.TargetAlbumID != nil {
			albumID = *s.TargetAlbumID
		}
		args = append(args, s.UUID, string(s.TargetType), albumID, boolToInt(s.AllowDownload))
	}
	// Seven placeholders: owner.Hub, owner.UserID, mediaID for the
	// top-level hidden_at IS NULL guard, then mediaID twice in the
	// media_set EXISTS (direct id + paired_with_id lookup), then
	// mediaID twice again in the album_live EXISTS.
	args = append(args,
		owner.Hub, owner.UserID,
		mediaID,
		mediaID, mediaID,
		mediaID, mediaID,
	)

	// The OR-paired_with_id branch is wrapped together with the direct
	// id match inside each EXISTS so the surrounding AND-joined
	// predicates (owner filter, target_type/target_album_id pinning)
	// continue to bind. SQL precedence makes AND tighter than OR; the
	// inner parentheses prevent the sidecar branch from short-circuiting
	// the outer guards.
	//
	// hidden_at IS NULL guard: the EXISTS clauses still hold for
	// scope_media / album_media membership. The top-level AND on the
	// media row for the requested id ensures a hidden photo (or a sidecar
	// whose primary is hidden) cannot pass through to the grantee.
	// Grantee reads have no IncludeHidden escape hatch.
	q := `
WITH validated(uuid, target_type, target_album_id, allow_download) AS (
    VALUES ` + strings.Join(valRows, ",") + `
)
SELECT v.uuid, v.target_album_id, v.allow_download
  FROM validated v
  JOIN scopes s ON s.uuid = v.uuid
 WHERE s.owner_hub = ? AND s.owner_user_id = ?
   AND EXISTS (SELECT 1 FROM media WHERE id = ? AND hidden_at IS NULL)
   AND (
         (v.target_type = 'media_set' AND EXISTS (
             SELECT 1 FROM scope_media sm
              WHERE sm.scope_uuid = v.uuid
                AND (sm.media_id = ?
                     OR sm.media_id = (SELECT paired_with_id FROM media WHERE id = ?))
         ))
      OR (v.target_type = 'album_live' AND EXISTS (
             SELECT 1 FROM album_media am
              WHERE am.album_id = v.target_album_id
                AND (am.media_id = ?
                     OR am.media_id = (SELECT paired_with_id FROM media WHERE id = ?))
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
			p        AccessPath
			albumID  sql.NullString
			allowInt int
		)
		if err := rows.Scan(&p.ScopeUUID, &albumID, &allowInt); err != nil {
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

// SharedMediaRow is one row from ListSharedMediaIDs.
type SharedMediaRow struct {
	MediaID     string
	DisplayTime time.Time
	CanDownload bool
}

// SharedMediaCursor paginates ListSharedMediaIDs by (display_time, id).
// Limit is clamped by the caller (service tier); the repo itself does
// not enforce a cap. Limit <= 0 means "no LIMIT clause."
type SharedMediaCursor struct {
	AfterDisplayTime time.Time
	AfterID          string
	Limit            int
}

// ListSharedMediaIDs returns the deduped union of media visible via the
// retained-owner validated slice. Rows are ordered display_time DESC,
// id ASC (display_time = COALESCE(timestamp, imported_at)). can_download
// is MAX(allow_download) across covering scopes. When albumID is
// non-empty, the result is further restricted to album_media members of
// that album.
func (r *Repo) ListSharedMediaIDs(
	ctx context.Context,
	validated []Scope,
	owner owners.Principal,
	albumID string,
	cursor SharedMediaCursor,
) ([]SharedMediaRow, error) {
	if len(validated) == 0 {
		return nil, nil
	}
	valRows := make([]string, 0, len(validated))
	args := make([]any, 0, len(validated)*4+8)
	for _, s := range validated {
		valRows = append(valRows, "(?, ?, ?, ?)")
		var albumArg any
		if s.TargetAlbumID != nil {
			albumArg = *s.TargetAlbumID
		}
		args = append(args, s.UUID, string(s.TargetType), albumArg, boolToInt(s.AllowDownload))
	}
	args = append(args, owner.Hub, owner.UserID)

	hasCursor := 0
	if !cursor.AfterDisplayTime.IsZero() || cursor.AfterID != "" {
		hasCursor = 1
	}
	args = append(args, hasCursor, cursor.AfterDisplayTime, cursor.AfterDisplayTime, cursor.AfterID)

	albumPredicate := ""
	if albumID != "" {
		albumPredicate = ` AND EXISTS (
            SELECT 1 FROM album_media am2
             WHERE am2.album_id = ? AND am2.media_id = m.id
        )`
		args = append(args, albumID)
	}

	limitClause := ""
	if cursor.Limit > 0 {
		limitClause = " LIMIT ?"
		args = append(args, cursor.Limit)
	}

	q := `
WITH validated(uuid, target_type, target_album_id, allow_download) AS (
    VALUES ` + strings.Join(valRows, ",") + `
)
SELECT m.id,
       COALESCE(m.timestamp, m.imported_at) AS display_time,
       MAX(covers.allow_download) AS can_download
  FROM media m
  JOIN (
      SELECT sm.media_id AS media_id, v.allow_download
        FROM scope_media sm
        JOIN validated v ON v.uuid = sm.scope_uuid
       WHERE v.target_type = 'media_set'
      UNION ALL
      SELECT am.media_id AS media_id, v.allow_download
        FROM album_media am
        JOIN validated v ON v.target_album_id = am.album_id
       WHERE v.target_type = 'album_live'
  ) covers ON covers.media_id = m.id
 WHERE m.owner_hub = ? AND m.owner_user_id = ?
   AND m.hidden_at IS NULL
   AND (
         ? = 0
      OR COALESCE(m.timestamp, m.imported_at) < ?
      OR (COALESCE(m.timestamp, m.imported_at) = ? AND m.id > ?)
       )` + albumPredicate + `
 GROUP BY m.id
 ORDER BY display_time DESC, m.id ASC` + limitClause

	rows, err := r.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list shared media ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]SharedMediaRow, 0, 32)
	for rows.Next() {
		var (
			row         SharedMediaRow
			displayTime string
			allowInt    int
		)
		if err := rows.Scan(&row.MediaID, &displayTime, &allowInt); err != nil {
			return nil, fmt.Errorf("scan shared media row: %w", err)
		}
		dt, perr := parseSQLiteTimeString(displayTime)
		if perr != nil {
			return nil, fmt.Errorf("parse display_time: %w", perr)
		}
		row.DisplayTime = dt
		row.CanDownload = allowInt != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

// SharedAlbumRow is one row from ListSharedAlbumIDs.
type SharedAlbumRow struct {
	AlbumID     string
	CanDownload bool
}

// ListSharedAlbumIDs returns distinct album ids authorised by the
// album_live subset of validated, with MAX(allow_download) collapsed
// per album. Media_set scopes are silently ignored (grantees see those
// as media, not albums). The owner-hub / owner-user_id predicate on
// albums is a belt-and-braces guard against resolver bugs.
func (r *Repo) ListSharedAlbumIDs(
	ctx context.Context,
	validated []Scope,
	owner owners.Principal,
) ([]SharedAlbumRow, error) {
	if len(validated) == 0 {
		return nil, nil
	}
	live := make([]Scope, 0, len(validated))
	for _, s := range validated {
		if s.TargetType == TargetAlbumLive && s.TargetAlbumID != nil {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return nil, nil
	}
	valRows := make([]string, 0, len(live))
	args := make([]any, 0, len(live)*3+2)
	for _, s := range live {
		valRows = append(valRows, "(?, ?, ?)")
		args = append(args, s.UUID, *s.TargetAlbumID, boolToInt(s.AllowDownload))
	}
	args = append(args, owner.Hub, owner.UserID)

	q := `
WITH validated(uuid, target_album_id, allow_download) AS (
    VALUES ` + strings.Join(valRows, ",") + `
)
SELECT a.id, MAX(v.allow_download)
  FROM validated v
  JOIN albums a ON a.id = v.target_album_id
 WHERE a.owner_hub = ? AND a.owner_user_id = ?
 GROUP BY a.id
`
	rows, err := r.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list shared album ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]SharedAlbumRow, 0, len(live))
	for rows.Next() {
		var (
			row      SharedAlbumRow
			allowInt int
		)
		if err := rows.Scan(&row.AlbumID, &allowInt); err != nil {
			return nil, fmt.Errorf("scan shared album row: %w", err)
		}
		row.CanDownload = allowInt != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

// CountPendingByOp returns the number of scopes whose broker work for
// op is still in flight. op ∈ {"publish","revoke"}; "publish" maps to
// broker_status='pending' (queued for PublishScope), "revoke" maps to
// broker_status='revoking' (queued for RevokeScope). Used as the
// closure source for the obs.Metrics SharePendingByOp gauge — gauges
// are scrape-time best-effort, so callers wrap this in a short-lived
// ctx and treat errors as "report 0".
func (r *Repo) CountPendingByOp(ctx context.Context, op string) (int64, error) {
	var status BrokerStatus
	switch op {
	case "publish":
		status = StatusPending
	case "revoke":
		status = StatusRevoking
	default:
		return 0, fmt.Errorf("CountPendingByOp: unknown op %q", op)
	}
	var n int64
	err := r.ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scopes WHERE broker_status = ?`, string(status)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count scopes by broker_status: %w", err)
	}
	return n, nil
}

// CountSharedMediaByScope returns the number of media covered by the
// scope: scope_media rows for media_set, album_media rows for
// album_live. Returns errs.ErrNotFound if the scope row does not exist.
func (r *Repo) CountSharedMediaByScope(ctx context.Context, scopeUUID string) (int, error) {
	var (
		targetType TargetType
		albumID    sql.NullString
	)
	err := r.ro.QueryRowContext(ctx,
		`SELECT target_type, target_album_id FROM scopes WHERE uuid = ?`, scopeUUID,
	).Scan(&targetType, &albumID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: scope uuid=%s", errs.ErrNotFound, scopeUUID)
	}
	if err != nil {
		return 0, fmt.Errorf("count scope media: load scope: %w", err)
	}
	switch targetType {
	case TargetMediaSet:
		var n int
		if err := r.ro.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM scope_media sm
			   JOIN media m ON m.id = sm.media_id
			  WHERE sm.scope_uuid = ? AND m.hidden_at IS NULL`, scopeUUID,
		).Scan(&n); err != nil {
			return 0, fmt.Errorf("count scope_media: %w", err)
		}
		return n, nil
	case TargetAlbumLive:
		if !albumID.Valid {
			return 0, fmt.Errorf("album_live scope %s missing target_album_id", scopeUUID)
		}
		var n int
		if err := r.ro.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM album_media am
			  JOIN media m ON m.id = am.media_id
			 WHERE am.album_id = ? AND m.hidden_at IS NULL`, albumID.String,
		).Scan(&n); err != nil {
			return 0, fmt.Errorf("count album_media: %w", err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("unknown target_type %q", targetType)
	}
}

// CountSharedMediaByScopes returns a map of scope_uuid → count of
// scope_media members for the given scope UUIDs. Scopes that exist in
// the scopes table are present in the result map (with 0 if they have
// no scope_media rows, which is normal for album_live scopes since
// their membership is derived from album_media). UUIDs that do not
// exist in the scopes table are absent from the result map. Chunked
// at 250 UUIDs (500 bind vars) to stay safely under SQLite's 999-cap.
//
// The two-query pattern (existence check, then counts) is what makes
// the absent-vs-zero distinction work: an album_live scope that has no
// scope_media rows still ends up in the map at 0; a uuid that is not
// in the scopes table at all is absent. Callers that want full media
// coverage for album_live should use CountSharedMediaByScope (singular)
// which dispatches on target_type and counts album_media.
func (r *Repo) CountSharedMediaByScopes(ctx context.Context, uuids []string) (map[string]int, error) {
	out := map[string]int{}
	if len(uuids) == 0 {
		return out, nil
	}
	const chunkSize = 250
	for start := 0; start < len(uuids); start += chunkSize {
		end := min(start+chunkSize, len(uuids))
		chunk := uuids[start:end]
		if err := r.countSharedMediaChunk(ctx, chunk, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// countSharedMediaChunk runs the two-query exist+count pass for one
// chunk and merges results into out. Extracted so the outer loop in
// CountSharedMediaByScopes stays under the cyclomatic-complexity cap.
func (r *Repo) countSharedMediaChunk(ctx context.Context, chunk []string, out map[string]int) error {
	placeholders := strings.Repeat("?,", len(chunk))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(chunk))
	for i, u := range chunk {
		args[i] = u
	}
	existsRows, err := r.ro.QueryContext(ctx,
		`SELECT uuid FROM scopes WHERE uuid IN (`+placeholders+`)`, args...)
	if err != nil {
		return fmt.Errorf("share: CountSharedMediaByScopes exists: %w", err)
	}
	if err := drainExistsRows(existsRows, out); err != nil {
		return err
	}
	countRows, err := r.ro.QueryContext(ctx,
		`SELECT scope_uuid, COUNT(*) FROM scope_media
		  WHERE scope_uuid IN (`+placeholders+`)
		  GROUP BY scope_uuid`, args...)
	if err != nil {
		return fmt.Errorf("share: CountSharedMediaByScopes: %w", err)
	}
	return drainCountRows(countRows, out)
}

// drainExistsRows reads a "SELECT uuid" stream into out, initialising
// every existing scope to 0. Closes rows on return.
func drainExistsRows(rows *sql.Rows, out map[string]int) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return fmt.Errorf("share: CountSharedMediaByScopes exists scan: %w", err)
		}
		out[u] = 0
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("share: CountSharedMediaByScopes exists iter: %w", err)
	}
	return nil
}

// drainCountRows reads a "(scope_uuid, COUNT(*))" stream and overwrites
// existing entries in out. Closes rows on return. Entries that are not
// already present are also written, but the canonical path is via
// drainExistsRows first so the album_live (no scope_media) case stays
// at 0.
func drainCountRows(rows *sql.Rows, out map[string]int) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			u string
			c int
		)
		if err := rows.Scan(&u, &c); err != nil {
			return fmt.Errorf("share: CountSharedMediaByScopes scan: %w", err)
		}
		out[u] = c
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("share: CountSharedMediaByScopes iter: %w", err)
	}
	return nil
}

// AlbumSummary is the minimal album view attached to ExpandedScope for
// album_live scopes. Kept here (rather than reusing album.AlbumListItem)
// to avoid a share→album dependency in this direction.
type AlbumSummary struct {
	ID        string
	Name      string
	ItemCount int
	UpdatedAt time.Time
}

// ExpandedScope is the pure materializer output used by PreviewScope.
// MediaIDs is always populated: frozen membership for media_set, live
// album_media order for album_live. Album is non-nil iff target_type
// == album_live.
type ExpandedScope struct {
	Scope    Scope
	MediaIDs []string
	Album    *AlbumSummary
}

// ExpandScope reads a scope and its materialised membership without
// any grantee-identity plumbing. Callers must have already performed
// the owner-scoped auth check (see service.ShareService.PreviewScope).
func (r *Repo) ExpandScope(ctx context.Context, scopeUUID string) (ExpandedScope, error) {
	detail, err := r.GetByUUID(ctx, scopeUUID)
	if err != nil {
		return ExpandedScope{}, err
	}
	exp := ExpandedScope{Scope: detail.Scope}
	switch detail.TargetType {
	case TargetMediaSet:
		exp.MediaIDs = slices.Clone(detail.MediaIDs)
	case TargetAlbumLive:
		if detail.TargetAlbumID == nil {
			return ExpandedScope{}, fmt.Errorf("album_live scope %s has no target_album_id", scopeUUID)
		}
		mediaIDs, err := r.listAlbumMediaIDs(ctx, *detail.TargetAlbumID)
		if err != nil {
			return ExpandedScope{}, err
		}
		exp.MediaIDs = mediaIDs
		summary, err := r.albumSummary(ctx, *detail.TargetAlbumID)
		if err != nil {
			return ExpandedScope{}, err
		}
		exp.Album = &summary
	default:
		return ExpandedScope{}, fmt.Errorf("unknown target_type %q", detail.TargetType)
	}
	return exp, nil
}

// listAlbumMediaIDs returns album_media rows ordered to match
// album.Repo.ListMedia's default "added" mode: added_at DESC,
// media_id DESC. A DESC tiebreaker matters because batched
// AddMedia inserts share a single added_at timestamp; an ASC
// tiebreaker here would make the owner preview disagree with
// what the grantee and the owner UI render.
func (r *Repo) listAlbumMediaIDs(ctx context.Context, albumID string) ([]string, error) {
	rows, err := r.ro.QueryContext(ctx,
		`SELECT am.media_id FROM album_media am
		   JOIN media m ON m.id = am.media_id
          WHERE am.album_id = ? AND m.hidden_at IS NULL
          ORDER BY am.added_at DESC, am.media_id DESC`, albumID)
	if err != nil {
		return nil, fmt.Errorf("list album media ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan album media id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter album media ids: %w", err)
	}
	return out, nil
}

// albumSummary reads just the name / updated_at / item_count for one
// album. Returns errs.ErrNotFound when the album row is missing.
func (r *Repo) albumSummary(ctx context.Context, albumID string) (AlbumSummary, error) {
	var s AlbumSummary
	err := r.ro.QueryRowContext(ctx,
		`SELECT a.id, a.name, a.updated_at,
                (SELECT COUNT(*) FROM album_media am
                   JOIN media m ON m.id = am.media_id
                  WHERE am.album_id = a.id AND m.hidden_at IS NULL)
           FROM albums a WHERE a.id = ?`, albumID,
	).Scan(&s.ID, &s.Name, &s.UpdatedAt, &s.ItemCount)
	if errors.Is(err, sql.ErrNoRows) {
		return AlbumSummary{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, albumID)
	}
	if err != nil {
		return AlbumSummary{}, fmt.Errorf("read album summary: %w", err)
	}
	return s, nil
}

// sqliteTimestampFormats mirrors mattn/go-sqlite3's
// SQLiteTimestampFormats slice (v1.14.44, sqlite3.go:312-324) in the
// same try-order mattn's own column auto-decode uses
// (sqlite3.go:2622). We inline rather than import the driver
// constant: this file is policy-neutral with respect to which driver
// the *sql.DB pools came from, and importing mattn here just to read
// a slice would couple the share repo to a specific driver build tag.
//
// On upgrade: re-verify against
// github.com/mattn/go-sqlite3@<version>/sqlite3.go's
// SQLiteTimestampFormats and refresh both the slice and this comment.
var sqliteTimestampFormats = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseSQLiteTimeString parses the string mattn/go-sqlite3 returns when
// a TIMESTAMP value passes through an expression (COALESCE, CASE, …)
// and loses its column declaration type. Mattn's column-decode path
// only auto-parses values into time.Time when the originating column
// is declared TIMESTAMP/DATETIME/DATE; expression results have no
// declared type, so the driver returns the underlying TEXT bytes
// unchanged.
//
// Mattn writes new timestamps with SQLiteTimestampFormats[0] but
// happily reads any of the nine layouts in the list — including older
// rows persisted before the format list was extended, or rows written
// by external tools (sqlite3 CLI, sqldiff, manual ATTACH+INSERT).
// We mirror the same try-in-order behaviour by iterating
// sqliteTimestampFormats and returning the first format that parses;
// if all nine fail we return the last error so the caller surfaces a
// concrete parse error rather than a nil-time success.
//
// We pass time.UTC as the default location to match
// mattn (sqlite3.go:2622-2623): for layouts that omit a TZ offset,
// the recovered time.Time lands in UTC.
//
// UTC invariant: every TIMESTAMP we store is UTC; the returned
// time.Time preserves the offset that time.ParseInLocation recovers
// (or defaults to UTC), so downstream comparisons against other UTC
// times stay correct.
func parseSQLiteTimeString(s string) (time.Time, error) {
	// Mirror mattn's pre-parse trim of a trailing "Z" suffix
	// (sqlite3.go:2621) so an explicit-Zulu layout (e.g. from external
	// tools) doesn't fall off the end of the format list.
	s = strings.TrimSuffix(s, "Z")
	var lastErr error
	for _, format := range sqliteTimestampFormats {
		t, err := time.ParseInLocation(format, s, time.UTC)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
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

package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/errs"
)

// Repo is a SQLite-backed store of scopes + scope_media rows. Split
// read/write pool: writes go through rw, reads through ro.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool, ro the reader.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// All scope columns in a canonical order used by SELECT and Scan.
const scopeSelect = `
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
		`SELECT `+scopeSelect+` FROM scopes WHERE uuid = ?`, uuidStr)
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

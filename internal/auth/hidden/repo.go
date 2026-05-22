// Package hidden provides the repository for the hidden-privacy auth domain:
// credentials (passcode hashes), sessions, failure log, and lockout records.
package hidden

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// Credential holds the passcode hash for one principal.
type Credential struct {
	Principal    owners.Principal
	PasscodeHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Session is one issued auth token (stored as its SHA-256 digest).
type Session struct {
	TokenSHA256 []byte
	Principal   owners.Principal
	IssuedAt    time.Time
	ExpiresAt   time.Time
	RevokedAt   *time.Time
}

// Lockout records when a principal is locked out due to repeated failures.
type Lockout struct {
	Principal   owners.Principal
	LockedUntil time.Time
	UpdatedAt   time.Time
}

// Repo is the SQLite-backed store for hidden-privacy auth tables.
// Writes go through rw; reads through ro.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool, ro the reader pool.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// GetCredential returns the credential for principal. Returns errs.ErrNotFound on miss.
func (r *Repo) GetCredential(ctx context.Context, p owners.Principal) (*Credential, error) {
	var c Credential
	err := r.ro.QueryRowContext(ctx,
		`SELECT principal_hub, principal_user_id, passcode_hash, created_at, updated_at
		   FROM auth_hidden_credential
		  WHERE principal_hub = ? AND principal_user_id = ?`,
		p.Hub, p.UserID,
	).Scan(&c.Principal.Hub, &c.Principal.UserID, &c.PasscodeHash, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: credential principal=%s", errs.ErrNotFound, p)
	}
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}
	return &c, nil
}

// UpsertCredential inserts or updates the passcode hash for principal.
// On insert, created_at is set to now; updated_at is always set to now.
func (r *Repo) UpsertCredential(ctx context.Context, p owners.Principal, hash string, now time.Time) error {
	_, err := r.rw.ExecContext(ctx, `
		INSERT INTO auth_hidden_credential
		    (principal_hub, principal_user_id, passcode_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(principal_hub, principal_user_id) DO UPDATE SET
		    passcode_hash = excluded.passcode_hash,
		    updated_at    = excluded.updated_at`,
		p.Hub, p.UserID, hash, now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert credential: %w", err)
	}
	return nil
}

// InsertCredential inserts a new credential for principal, failing closed
// if one already exists. Returns errs.ErrAlreadyExists on conflict so two
// concurrent Setup calls can't both think they won — only the first
// commit takes effect, the second sees ErrAlreadyExists.
func (r *Repo) InsertCredential(ctx context.Context, p owners.Principal, hash string, now time.Time) error {
	res, err := r.rw.ExecContext(ctx, `
		INSERT INTO auth_hidden_credential
		    (principal_hub, principal_user_id, passcode_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		p.Hub, p.UserID, hash, now, now,
	)
	if err != nil {
		return fmt.Errorf("insert credential: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("insert credential rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: credential principal=%s", errs.ErrAlreadyExists, p)
	}
	return nil
}

// DeleteCredential removes the credential for principal.
func (r *Repo) DeleteCredential(ctx context.Context, p owners.Principal) error {
	_, err := r.rw.ExecContext(ctx,
		`DELETE FROM auth_hidden_credential WHERE principal_hub = ? AND principal_user_id = ?`,
		p.Hub, p.UserID,
	)
	if err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}

// InsertSession stores a new session row.
func (r *Repo) InsertSession(ctx context.Context, s Session) error {
	_, err := r.rw.ExecContext(ctx,
		`INSERT INTO auth_hidden_session
		    (token_sha256, principal_hub, principal_user_id, issued_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		s.TokenSHA256, s.Principal.Hub, s.Principal.UserID, s.IssuedAt, s.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// LookupActiveSession returns the session for tokenSHA256 if it is not revoked
// and has not expired as of now. Returns errs.ErrNotFound otherwise.
func (r *Repo) LookupActiveSession(ctx context.Context, tokenSHA256 []byte, now time.Time) (*Session, error) {
	var s Session
	var revokedAt sql.NullTime
	err := r.ro.QueryRowContext(ctx,
		`SELECT token_sha256, principal_hub, principal_user_id, issued_at, expires_at, revoked_at
		   FROM auth_hidden_session
		  WHERE token_sha256 = ?
		    AND revoked_at IS NULL
		    AND expires_at > ?`,
		tokenSHA256, now,
	).Scan(&s.TokenSHA256, &s.Principal.Hub, &s.Principal.UserID,
		&s.IssuedAt, &s.ExpiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: session not found or inactive", errs.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("lookup active session: %w", err)
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		s.RevokedAt = &t
	}
	return &s, nil
}

// RevokeSession sets revoked_at on the token to now. Idempotent: a second call
// on an already-revoked token is a no-op (the original revoke timestamp wins).
func (r *Repo) RevokeSession(ctx context.Context, tokenSHA256 []byte, now time.Time) error {
	_, err := r.rw.ExecContext(ctx,
		`UPDATE auth_hidden_session SET revoked_at = ? WHERE token_sha256 = ? AND revoked_at IS NULL`,
		now, tokenSHA256,
	)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeAllSessionsForPrincipal sets revoked_at = now on all active sessions for principal.
func (r *Repo) RevokeAllSessionsForPrincipal(ctx context.Context, p owners.Principal, now time.Time) error {
	_, err := r.rw.ExecContext(ctx,
		`UPDATE auth_hidden_session SET revoked_at = ?
		  WHERE principal_hub = ? AND principal_user_id = ? AND revoked_at IS NULL`,
		now, p.Hub, p.UserID,
	)
	if err != nil {
		return fmt.Errorf("revoke all sessions for principal: %w", err)
	}
	return nil
}

// SweepExpiredSessions marks revoked_at on rows that have passed their
// expires_at but have not yet been explicitly revoked. Uses <= so the
// sweep matches LookupActiveSession's > predicate at the boundary —
// without this, a session at exactly expires_at == now would be inactive
// to lookups but un-reclaimed by the sweeper.
func (r *Repo) SweepExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := r.rw.ExecContext(ctx,
		`UPDATE auth_hidden_session SET revoked_at = ?
		  WHERE expires_at <= ? AND revoked_at IS NULL`,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("sweep expired sessions: %w", err)
	}
	return nil
}

// InsertFailure records one failed authentication attempt for principal at now.
func (r *Repo) InsertFailure(ctx context.Context, p owners.Principal, now time.Time) error {
	_, err := r.rw.ExecContext(ctx,
		`INSERT INTO auth_hidden_failure (principal_hub, principal_user_id, occurred_at)
		 VALUES (?, ?, ?)`,
		p.Hub, p.UserID, now,
	)
	if err != nil {
		return fmt.Errorf("insert failure: %w", err)
	}
	return nil
}

// CountRecentFailures returns the number of failures for principal since the given time.
func (r *Repo) CountRecentFailures(ctx context.Context, p owners.Principal, since time.Time) (int, error) {
	var n int
	err := r.ro.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM auth_hidden_failure
		  WHERE principal_hub = ? AND principal_user_id = ? AND occurred_at >= ?`,
		p.Hub, p.UserID, since,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count recent failures: %w", err)
	}
	return n, nil
}

// PurgeOldFailures deletes failure rows with occurred_at < before.
func (r *Repo) PurgeOldFailures(ctx context.Context, before time.Time) error {
	_, err := r.rw.ExecContext(ctx,
		`DELETE FROM auth_hidden_failure WHERE occurred_at < ?`, before,
	)
	if err != nil {
		return fmt.Errorf("purge old failures: %w", err)
	}
	return nil
}

// DeleteAllFailuresForPrincipal removes all failure rows for principal.
// Used by the service to reset failure state on successful authentication.
func (r *Repo) DeleteAllFailuresForPrincipal(ctx context.Context, p owners.Principal) error {
	_, err := r.rw.ExecContext(ctx,
		`DELETE FROM auth_hidden_failure WHERE principal_hub = ? AND principal_user_id = ?`,
		p.Hub, p.UserID,
	)
	if err != nil {
		return fmt.Errorf("delete all failures for principal: %w", err)
	}
	return nil
}

// GetLockout returns the lockout record for principal. Returns errs.ErrNotFound on miss.
func (r *Repo) GetLockout(ctx context.Context, p owners.Principal) (*Lockout, error) {
	var l Lockout
	err := r.ro.QueryRowContext(ctx,
		`SELECT principal_hub, principal_user_id, locked_until, updated_at
		   FROM auth_hidden_lockout
		  WHERE principal_hub = ? AND principal_user_id = ?`,
		p.Hub, p.UserID,
	).Scan(&l.Principal.Hub, &l.Principal.UserID, &l.LockedUntil, &l.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: lockout principal=%s", errs.ErrNotFound, p)
	}
	if err != nil {
		return nil, fmt.Errorf("get lockout: %w", err)
	}
	return &l, nil
}

// UpsertLockout inserts or updates the lockout record for principal.
func (r *Repo) UpsertLockout(ctx context.Context, p owners.Principal, lockedUntil, now time.Time) error {
	_, err := r.rw.ExecContext(ctx, `
		INSERT INTO auth_hidden_lockout
		    (principal_hub, principal_user_id, locked_until, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(principal_hub, principal_user_id) DO UPDATE SET
		    locked_until = excluded.locked_until,
		    updated_at   = excluded.updated_at`,
		p.Hub, p.UserID, lockedUntil, now,
	)
	if err != nil {
		return fmt.Errorf("upsert lockout: %w", err)
	}
	return nil
}

// DeleteLockout removes the lockout record for principal.
func (r *Repo) DeleteLockout(ctx context.Context, p owners.Principal) error {
	_, err := r.rw.ExecContext(ctx,
		`DELETE FROM auth_hidden_lockout WHERE principal_hub = ? AND principal_user_id = ?`,
		p.Hub, p.UserID,
	)
	if err != nil {
		return fmt.Errorf("delete lockout: %w", err)
	}
	return nil
}

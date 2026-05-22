// Package usersettings persists per-principal, non-secret UI preferences
// (theme, grid density, etc.). Values are opaque JSON strings; callers
// are responsible for marshalling and unmarshalling.
package usersettings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/fotobank/internal/owners"
)

// Repo is a SQLite-backed store of user_settings rows. It uses a split
// read/write pool: writes go through rw, reads through ro.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool, ro the reader.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// Upsert writes or replaces (principal, key) -> valueJSON.
func (r *Repo) Upsert(ctx context.Context, p owners.Principal, key, valueJSON string) error {
	_, err := r.rw.ExecContext(ctx, `
		INSERT INTO user_settings (principal_hub, principal_user_id, key, value, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (principal_hub, principal_user_id, key)
		DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, p.Hub, p.UserID, key, valueJSON, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("user_settings upsert: %w", err)
	}
	return nil
}

// Get returns (value, true) when the row exists, otherwise ("", false).
func (r *Repo) Get(ctx context.Context, p owners.Principal, key string) (string, bool, error) {
	var v string
	err := r.ro.QueryRowContext(ctx, `
		SELECT value FROM user_settings
		WHERE principal_hub = ? AND principal_user_id = ? AND key = ?
	`, p.Hub, p.UserID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("user_settings get: %w", err)
	}
	return v, true, nil
}

// Delete removes (principal, key). Missing keys are not an error.
func (r *Repo) Delete(ctx context.Context, p owners.Principal, key string) error {
	_, err := r.rw.ExecContext(ctx, `
		DELETE FROM user_settings
		WHERE principal_hub = ? AND principal_user_id = ? AND key = ?
	`, p.Hub, p.UserID, key)
	if err != nil {
		return fmt.Errorf("user_settings delete: %w", err)
	}
	return nil
}

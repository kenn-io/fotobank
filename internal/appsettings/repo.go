// Package appsettings persists server-global, non-secret runtime
// settings. Values are JSON-encoded by callers; the repo treats them
// as opaque strings.
package appsettings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// Row mirrors one app_settings row.
type Row struct {
	Key             string
	Value           string
	UpdatedAt       time.Time
	UpdatedByHub    *string
	UpdatedByUserID *string
}

// Repo is a SQLite-backed global settings store. Writes use rw; reads
// use ro so settings display does not contend with the single writer.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo over the supplied pools.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// Upsert writes key -> value. When by is nil, updated_by_* columns are
// set NULL.
func (r *Repo) Upsert(ctx context.Context, key, value string, by *owners.Principal) error {
	var hub, userID any
	if by != nil {
		hub = by.Hub
		userID = by.UserID
	}
	_, err := r.rw.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at, updated_by_hub, updated_by_user_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at,
			updated_by_hub = excluded.updated_by_hub,
			updated_by_user_id = excluded.updated_by_user_id
	`, key, value, time.Now().UTC(), hub, userID)
	if err != nil {
		return fmt.Errorf("app_settings upsert %q: %w", key, err)
	}
	return nil
}

// Get returns a row when key exists.
func (r *Repo) Get(ctx context.Context, key string) (Row, bool, error) {
	row, err := scanRow(r.ro.QueryRowContext(ctx, `
		SELECT key, value, updated_at, updated_by_hub, updated_by_user_id
		  FROM app_settings WHERE key = ?
	`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, false, nil
	}
	if err != nil {
		return Row{}, false, fmt.Errorf("app_settings get %q: %w", key, err)
	}
	return row, true, nil
}

// List returns every row sorted by key.
func (r *Repo) List(ctx context.Context) ([]Row, error) {
	rows, err := r.ro.QueryContext(ctx, `
		SELECT key, value, updated_at, updated_by_hub, updated_by_user_id
		  FROM app_settings ORDER BY key
	`)
	if err != nil {
		return nil, fmt.Errorf("app_settings list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Row{}
	for rows.Next() {
		row, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("app_settings scan: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("app_settings rows: %w", err)
	}
	return out, nil
}

// Delete removes key. Missing keys are not an error.
func (r *Repo) Delete(ctx context.Context, key string) error {
	_, err := r.rw.ExecContext(ctx, `DELETE FROM app_settings WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("app_settings delete %q: %w", key, err)
	}
	return nil
}

// DeleteMany removes all keys. Empty input is a no-op.
func (r *Repo) DeleteMany(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, key := range keys {
		placeholders[i] = "?"
		args[i] = key
	}
	_, err := r.rw.ExecContext(ctx,
		`DELETE FROM app_settings WHERE key IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("app_settings delete many: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(s scanner) (Row, error) {
	var (
		row    Row
		hub    sql.NullString
		userID sql.NullString
	)
	if err := s.Scan(&row.Key, &row.Value, &row.UpdatedAt, &hub, &userID); err != nil {
		return Row{}, err
	}
	if hub.Valid {
		row.UpdatedByHub = &hub.String
	}
	if userID.Valid {
		row.UpdatedByUserID = &userID.String
	}
	return row, nil
}

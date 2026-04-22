package owners

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wesm/fotobank/internal/errs"
)

// Repo is a SQLite-backed store of registered Owners. It uses a split
// read/write pool: writes go through rw and reads through ro.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool and ro the reader pool.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

// Insert registers a new owner. Returns an error if the principal or storage
// key is already registered.
func (r *Repo) Insert(ctx context.Context, o Owner) error {
	_, err := r.rw.ExecContext(ctx,
		`INSERT INTO owners (hub, user_id, storage_key, display_handle, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		o.Principal.Hub, o.Principal.UserID, o.StorageKey, nullIfEmpty(o.DisplayHandle), o.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert owner: %w", err)
	}
	return nil
}

// GetByPrincipal looks up an owner by their principal. Returns errs.ErrNotFound
// if no such owner is registered.
func (r *Repo) GetByPrincipal(ctx context.Context, p Principal) (Owner, error) {
	var o Owner
	var handle sql.NullString
	err := r.ro.QueryRowContext(ctx,
		`SELECT hub, user_id, storage_key, display_handle, created_at
		   FROM owners WHERE hub = ? AND user_id = ?`,
		p.Hub, p.UserID,
	).Scan(&o.Principal.Hub, &o.Principal.UserID, &o.StorageKey, &handle, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Owner{}, errs.ErrNotFound
	}
	if err != nil {
		return Owner{}, fmt.Errorf("get owner: %w", err)
	}
	o.DisplayHandle = handle.String
	return o, nil
}

// List returns all registered owners ordered by (hub, user_id).
func (r *Repo) List(ctx context.Context) ([]Owner, error) {
	rows, err := r.ro.QueryContext(ctx,
		`SELECT hub, user_id, storage_key, display_handle, created_at FROM owners ORDER BY hub, user_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list owners: %w", err)
	}
	defer rows.Close()
	var out []Owner
	for rows.Next() {
		var o Owner
		var handle sql.NullString
		if err := rows.Scan(&o.Principal.Hub, &o.Principal.UserID, &o.StorageKey, &handle, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan owner: %w", err)
		}
		o.DisplayHandle = handle.String
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate owners: %w", err)
	}
	return out, nil
}

// Delete removes an owner by principal. Returns errs.ErrNotFound if no such
// owner is registered.
func (r *Repo) Delete(ctx context.Context, p Principal) error {
	res, err := r.rw.ExecContext(ctx,
		`DELETE FROM owners WHERE hub = ? AND user_id = ?`, p.Hub, p.UserID,
	)
	if err != nil {
		return fmt.Errorf("delete owner: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete owner rows affected: %w", err)
	}
	if n == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// DB returns the RO handle for callers that need ad-hoc queries
// spanning owners + other tables (e.g., "count media for owner").
func (r *Repo) DB() *sql.DB { return r.ro }

// UpdateDisplayHandle updates the display_handle of an existing owner.
// Returns errs.ErrNotFound if no such owner is registered.
func (r *Repo) UpdateDisplayHandle(ctx context.Context, p Principal, handle string) error {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE owners SET display_handle = ? WHERE hub = ? AND user_id = ?`,
		nullIfEmpty(handle), p.Hub, p.UserID,
	)
	if err != nil {
		return fmt.Errorf("update owner display handle: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update owner rows affected: %w", err)
	}
	if n == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

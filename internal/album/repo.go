package album

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/errs"
)

// Repo is a SQLite-backed store of album + album_media rows. It uses a
// split read/write pool: writes go through rw, reads through ro.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool, ro the reader.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

const albumSelect = `SELECT id, owner_hub, owner_user_id, name, created_at, updated_at FROM albums`

// Insert stores a new album row.
func (r *Repo) Insert(ctx context.Context, a Album) error {
	_, err := r.rw.ExecContext(ctx,
		`INSERT INTO albums (id, owner_hub, owner_user_id, name, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.Owner.Hub, a.Owner.UserID, a.Name, a.CreatedAt, a.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert album: %w", err)
	}
	return nil
}

// GetByID returns the bare album row. ErrNotFound if missing.
func (r *Repo) GetByID(ctx context.Context, id string) (Album, error) {
	row := r.ro.QueryRowContext(ctx, albumSelect+" WHERE id = ?", id)
	a, err := scanAlbum(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Album{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
	}
	if err != nil {
		return Album{}, fmt.Errorf("get album: %w", err)
	}
	return a, nil
}

// Rename updates name and updated_at. ErrNotFound if missing.
func (r *Repo) Rename(ctx context.Context, id, name string, now time.Time) error {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE albums SET name = ?, updated_at = ? WHERE id = ?`,
		name, now, id,
	)
	if err != nil {
		return fmt.Errorf("rename album: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rename rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
	}
	return nil
}

// Delete removes the album. album_media is cascaded by the FK ON DELETE
// CASCADE in the schema. ErrNotFound if missing.
func (r *Repo) Delete(ctx context.Context, id string) error {
	res, err := r.rw.ExecContext(ctx, `DELETE FROM albums WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete album: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAlbum(s rowScanner) (Album, error) {
	var a Album
	if err := s.Scan(
		&a.ID, &a.Owner.Hub, &a.Owner.UserID, &a.Name, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return Album{}, err
	}
	return a, nil
}

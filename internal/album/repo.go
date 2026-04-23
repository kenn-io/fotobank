package album

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
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

// GetDetailByID returns one album with derived ItemCount + Cover using
// the same count/cover subquery shape as ListByOwner. ErrNotFound on miss.
func (r *Repo) GetDetailByID(ctx context.Context, id string) (AlbumListItem, error) {
	const q = `
SELECT a.id, a.owner_hub, a.owner_user_id, a.name, a.created_at, a.updated_at,
       COALESCE(cnt.n, 0) AS item_count,
       cv.media_id, cv.thumb_version
  FROM albums a
  LEFT JOIN (
    SELECT album_id, COUNT(*) AS n
      FROM album_media
     WHERE album_id = ?
     GROUP BY album_id
  ) cnt ON cnt.album_id = a.id
  LEFT JOIN (
    SELECT am.album_id, am.media_id, m.thumb_version,
           ROW_NUMBER() OVER (
             PARTITION BY am.album_id
             ORDER BY am.added_at DESC, am.media_id ASC
           ) AS rn
      FROM album_media am
      JOIN media m ON m.id = am.media_id
     WHERE am.album_id = ?
       AND m.thumb_status = 'ready'
  ) cv ON cv.album_id = a.id AND cv.rn = 1
 WHERE a.id = ?;
`
	row := r.ro.QueryRowContext(ctx, q, id, id, id)
	item, err := scanAlbumListItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AlbumListItem{}, fmt.Errorf("%w: album id=%s", errs.ErrNotFound, id)
	}
	if err != nil {
		return AlbumListItem{}, fmt.Errorf("get album detail: %w", err)
	}
	return item, nil
}

func scanAlbumListItem(s rowScanner) (AlbumListItem, error) {
	var (
		item        AlbumListItem
		coverMedia  sql.NullString
		coverThumbV sql.NullInt64
	)
	if err := s.Scan(
		&item.ID, &item.Owner.Hub, &item.Owner.UserID, &item.Name,
		&item.CreatedAt, &item.UpdatedAt,
		&item.ItemCount,
		&coverMedia, &coverThumbV,
	); err != nil {
		return AlbumListItem{}, err
	}
	if coverMedia.Valid {
		item.Cover = &CoverRef{
			MediaID:      coverMedia.String,
			ThumbVersion: int(coverThumbV.Int64),
		}
	}
	return item, nil
}

// ListByOwner returns albums belonging to owner, paginated by limit /
// offset and ordered by updated_at DESC, id ASC. ItemCount and Cover
// are derived in the same statement.
func (r *Repo) ListByOwner(
	ctx context.Context,
	owner owners.Principal,
	limit, offset int,
) ([]AlbumListItem, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	const q = `
WITH owner_albums AS (
    SELECT id, owner_hub, owner_user_id, name, created_at, updated_at
      FROM albums
     WHERE owner_hub = ? AND owner_user_id = ?
     ORDER BY updated_at DESC, id ASC
     LIMIT ? OFFSET ?
)
SELECT oa.id, oa.owner_hub, oa.owner_user_id, oa.name, oa.created_at, oa.updated_at,
       COALESCE(cnt.n, 0) AS item_count,
       cv.media_id, cv.thumb_version
  FROM owner_albums oa
  LEFT JOIN (
    SELECT album_id, COUNT(*) AS n
      FROM album_media
     WHERE album_id IN (SELECT id FROM owner_albums)
     GROUP BY album_id
  ) cnt ON cnt.album_id = oa.id
  LEFT JOIN (
    SELECT am.album_id, am.media_id, m.thumb_version,
           ROW_NUMBER() OVER (
             PARTITION BY am.album_id
             ORDER BY am.added_at DESC, am.media_id ASC
           ) AS rn
      FROM album_media am
      JOIN media m ON m.id = am.media_id
     WHERE am.album_id IN (SELECT id FROM owner_albums)
       AND m.thumb_status = 'ready'
  ) cv ON cv.album_id = oa.id AND cv.rn = 1
 ORDER BY oa.updated_at DESC, oa.id ASC;
`
	rows, err := r.ro.QueryContext(ctx, q, owner.Hub, owner.UserID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list albums: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AlbumListItem
	for rows.Next() {
		item, err := scanAlbumListItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan album list item: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate albums: %w", err)
	}
	return out, nil
}

// AddMedia is mechanical and tolerant of empty input: the service layer
// enforces the 1..500 bound. Empty input → (0, 0, nil) without touching
// the DB. Uses a batched INSERT ... VALUES (?,?,?),... ON CONFLICT
// DO NOTHING; reports added = RowsAffected, alreadyPresent = len - added.
// Does NOT bump albums.updated_at; that is a non-goal for Plan D.
func (r *Repo) AddMedia(
	ctx context.Context,
	albumID string,
	mediaIDs []string,
	now time.Time,
) (added, alreadyPresent int, err error) {
	if len(mediaIDs) == 0 {
		return 0, 0, nil
	}
	// Build "(?,?,?),(?,?,?),..." with 3 args per row.
	values := make([]string, 0, len(mediaIDs))
	args := make([]any, 0, len(mediaIDs)*3)
	for _, mid := range mediaIDs {
		values = append(values, "(?,?,?)")
		args = append(args, albumID, mid, now)
	}
	q := `INSERT INTO album_media (album_id, media_id, added_at) VALUES ` +
		strings.Join(values, ",") +
		` ON CONFLICT (album_id, media_id) DO NOTHING`
	res, err := r.rw.ExecContext(ctx, q, args...)
	if err != nil {
		// The album_media owner-consistency trigger raises with text
		// "album and media must share owner" when the album and media
		// belong to different owners. The service layer's pre-flight
		// should catch this first; hitting it here means the service
		// invariant is violated. Wrap as ErrOwnerMismatch so the HTTP
		// layer can translate it to 500 + log.
		if isAlbumMediaOwnerMismatch(err) {
			return 0, 0, fmt.Errorf("%w: album=%s media=%v",
				errs.ErrOwnerMismatch, albumID, mediaIDs)
		}
		return 0, 0, fmt.Errorf("add album media: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("add album media rows affected: %w", err)
	}
	added = int(n)
	alreadyPresent = len(mediaIDs) - added
	return added, alreadyPresent, nil
}

// isAlbumMediaOwnerMismatch reports whether err is the SQLite trigger
// error raised by album_media_owner_consistency_{insert,update}. The
// trigger uses RAISE(ABORT, 'album and media must share owner') so the
// surface is stable and string-matchable.
func isAlbumMediaOwnerMismatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), "album and media must share owner")
}

// RemoveMedia removes one media_id from an album. ErrNotFound if the
// album does not exist or the pair (album_id, media_id) is absent —
// SQLite cannot distinguish the two cases at this layer, which is fine:
// the service's caller-owner check already gates cross-owner album IDs.
func (r *Repo) RemoveMedia(ctx context.Context, albumID, mediaID string) error {
	res, err := r.rw.ExecContext(ctx,
		`DELETE FROM album_media WHERE album_id = ? AND media_id = ?`,
		albumID, mediaID,
	)
	if err != nil {
		return fmt.Errorf("remove album media: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: album_media (album=%s, media=%s)",
			errs.ErrNotFound, albumID, mediaID)
	}
	return nil
}

const albumMediaMediaSelect = `SELECT
    m.id, m.owner_hub, m.owner_user_id, m.media_type, m.mime_type, m.path, m.original_filename,
    m.imported_at, m.timestamp, m.size, m.checksum,
    m.make, m.model, m.focal_length, m.shutter, m.width, m.height, m.iso, m.aperture,
    m.duration_ms,
    m.thumb_status, m.thumb_version, m.thumb_updated_at
FROM album_media am JOIN media m ON m.id = am.media_id`

// ListMedia returns paginated media rows that belong to albumID. The
// SortBy / SortAsc fields must be validated by the caller (service);
// the repo trusts SortBy ∈ {"added","imported"}.
func (r *Repo) ListMedia(
	ctx context.Context,
	albumID string,
	filter AlbumMediaFilter,
) ([]media.Media, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := max(filter.Offset, 0)
	direction := "DESC"
	if filter.SortAsc {
		direction = "ASC"
	}

	var orderBy string
	switch filter.SortBy {
	case "", "added":
		orderBy = "am.added_at " + direction + ", am.media_id " + direction
	case "imported":
		orderBy = "m.imported_at " + direction + " NULLS LAST, m.id " + direction
	default:
		// The service validates SortBy; hitting this means a caller bypassed it.
		return nil, fmt.Errorf("album.ListMedia: invalid SortBy %q", filter.SortBy)
	}

	q := albumMediaMediaSelect +
		" WHERE am.album_id = ?" +
		" ORDER BY " + orderBy +
		" LIMIT ? OFFSET ?"
	rows, err := r.ro.QueryContext(ctx, q, albumID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list album media: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []media.Media
	for rows.Next() {
		m, err := media.ScanMediaForAlbum(rows)
		if err != nil {
			return nil, fmt.Errorf("scan album media: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate album media: %w", err)
	}
	return out, nil
}

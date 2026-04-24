package media

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

// ErrDuplicateChecksum wraps errs.ErrAlreadyExists and indicates that
// the (owner, checksum) UNIQUE constraint fired on Insert. Callers that
// already treat this as a dedup race can keep using errors.Is against
// errs.ErrAlreadyExists; callers that need to distinguish it from a
// path collision should use errors.Is against ErrDuplicateChecksum.
var ErrDuplicateChecksum = fmt.Errorf("%w: duplicate checksum", errs.ErrAlreadyExists)

// ErrDuplicatePath wraps errs.ErrAlreadyExists and indicates that the
// (owner, path) UNIQUE constraint fired on Insert. The importer treats
// this as a phantom-row collision (DB has a row claiming the path but
// the NAS bytes are for different content).
var ErrDuplicatePath = fmt.Errorf("%w: duplicate path", errs.ErrAlreadyExists)

// Repo is a SQLite-backed store of media rows. It uses a split
// read/write pool: writes go through rw and reads through ro.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool and ro the reader pool.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

const mediaSelect = `SELECT
	id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
	imported_at, timestamp, size, checksum,
	make, model, focal_length, shutter, width, height, iso, aperture,
	duration_ms,
	thumb_status, thumb_version, thumb_updated_at
FROM media`

// mediaColumnsQualified is the m-prefixed projection used when the
// query joins a CTE that also has an `id` column. Keep column order
// identical to mediaSelect so scanMedia works unchanged.
const mediaColumnsQualified = `
    m.id, m.owner_hub, m.owner_user_id, m.media_type, m.mime_type, m.path, m.original_filename,
    m.imported_at, m.timestamp, m.size, m.checksum,
    m.make, m.model, m.focal_length, m.shutter, m.width, m.height, m.iso, m.aperture,
    m.duration_ms,
    m.thumb_status, m.thumb_version, m.thumb_updated_at`

const mediaInsert = `INSERT INTO media (
	id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
	imported_at, timestamp, size, checksum,
	make, model, focal_length, shutter, width, height, iso, aperture,
	duration_ms,
	thumb_status, thumb_version, thumb_updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// Insert stores a new media row. Returns errs.ErrAlreadyExists (wrapped)
// if a row already exists with the same (owner, checksum) or (owner, path).
func (r *Repo) Insert(ctx context.Context, m Media) error {
	_, err := r.rw.ExecContext(ctx, mediaInsert,
		m.ID,
		m.Owner.Hub,
		m.Owner.UserID,
		string(m.Type),
		m.MimeType,
		m.Path,
		nullStr(m.OriginalFilename),
		m.ImportedAt,
		nullTime(m.Timestamp),
		m.Size,
		m.Checksum,
		nullStr(m.Make),
		nullStr(m.Model),
		nullStr(m.FocalLength),
		nullStr(m.Shutter),
		nullInt(m.Width),
		nullInt(m.Height),
		nullInt(m.ISO),
		nullFloat(m.Aperture),
		nullInt64(m.DurationMs),
		m.ThumbStatus,
		m.ThumbVersion,
		nullTime(m.ThumbUpdatedAt),
	)
	if err != nil {
		if kind := uniqueViolationKind(err); kind != nil {
			return fmt.Errorf("%w: media (owner=%s, checksum=%s, path=%s)",
				kind, m.Owner, m.Checksum, m.Path)
		}
		return fmt.Errorf("insert media: %w", err)
	}
	return nil
}

// GetByID returns the media row with the given id. Returns errs.ErrNotFound
// if no such row exists.
func (r *Repo) GetByID(ctx context.Context, id string) (Media, error) {
	row := r.ro.QueryRowContext(ctx, mediaSelect+" WHERE id = ?", id)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	if err != nil {
		return Media{}, fmt.Errorf("get media: %w", err)
	}
	return m, nil
}

// GetByOwnerChecksum returns the media row for (owner, checksum). Returns
// errs.ErrNotFound if no such row exists.
func (r *Repo) GetByOwnerChecksum(ctx context.Context, p owners.Principal, checksum string) (Media, error) {
	row := r.ro.QueryRowContext(ctx,
		mediaSelect+" WHERE owner_hub = ? AND owner_user_id = ? AND checksum = ?",
		p.Hub, p.UserID, checksum,
	)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, fmt.Errorf("%w: media owner=%s checksum=%s", errs.ErrNotFound, p, checksum)
	}
	if err != nil {
		return Media{}, fmt.Errorf("get media by checksum: %w", err)
	}
	return m, nil
}

// GetByOwnerPath returns the media row for (owner, path). Returns
// errs.ErrNotFound if no such row exists.
func (r *Repo) GetByOwnerPath(ctx context.Context, p owners.Principal, path string) (Media, error) {
	row := r.ro.QueryRowContext(ctx,
		mediaSelect+" WHERE owner_hub = ? AND owner_user_id = ? AND path = ?",
		p.Hub, p.UserID, path,
	)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, fmt.Errorf("%w: media owner=%s path=%s", errs.ErrNotFound, p, path)
	}
	if err != nil {
		return Media{}, fmt.Errorf("get media by path: %w", err)
	}
	return m, nil
}

// GetByIDs returns media rows in the same order as ids. Missing ids are
// silently dropped. Empty input returns (nil, nil) without querying.
// Order preservation uses a VALUES-CTE carrying the caller-supplied
// position; the CTE's `id` column collides with media.id so the SELECT
// uses the m-prefixed projection.
func (r *Repo) GetByIDs(ctx context.Context, ids []string) ([]Media, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	valRows := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)*2)
	for i, id := range ids {
		valRows = append(valRows, "(?, ?)")
		args = append(args, id, i)
	}
	q := `
WITH ord(id, pos) AS (VALUES ` + strings.Join(valRows, ",") + `)
SELECT ` + mediaColumnsQualified + `
  FROM media m
  JOIN ord ON ord.id = m.id
 ORDER BY ord.pos
`
	rows, err := r.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get media by ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Media, 0, len(ids))
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan media by id: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter media by ids: %w", err)
	}
	return out, nil
}

const defaultListLimit = 1000

// List returns media rows matching the filter, ordered by timestamp then
// imported_at. Limit defaults to 1000 when <= 0; Offset is clamped to 0.
func (r *Repo) List(ctx context.Context, f ListFilter) ([]Media, error) {
	var (
		conds []string
		args  []any
	)
	conds = append(conds, "owner_hub = ?", "owner_user_id = ?")
	args = append(args, f.Owner.Hub, f.Owner.UserID)
	if f.Type != nil {
		conds = append(conds, "media_type = ?")
		args = append(args, string(*f.Type))
	}
	if f.DateFrom != nil {
		conds = append(conds, "timestamp >= ?")
		args = append(args, *f.DateFrom)
	}
	if f.DateTo != nil {
		conds = append(conds, "timestamp < ?")
		args = append(args, *f.DateTo)
	}

	direction := "ASC"
	if f.SortDesc {
		direction = "DESC"
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	offset := max(f.Offset, 0)

	query := mediaSelect +
		" WHERE " + strings.Join(conds, " AND ") +
		" ORDER BY timestamp " + direction + " NULLS LAST, imported_at " + direction + ", id " + direction +
		" LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := r.ro.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list media: %w", err)
	}
	defer rows.Close()

	var out []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan media: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media: %w", err)
	}
	return out, nil
}

// ListAll returns every media row for owner, paging through the database
// in batches of defaultListLimit. It is intended for bulk operations such
// as reconcile; user-facing queries should use List with an explicit Limit.
func (r *Repo) ListAll(ctx context.Context, owner owners.Principal) ([]Media, error) {
	var out []Media
	offset := 0
	for {
		page, err := r.List(ctx, ListFilter{Owner: owner, Limit: defaultListLimit, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < defaultListLimit {
			return out, nil
		}
		offset += defaultListLimit
	}
}

// Delete removes the media row with the given id. Returns errs.ErrNotFound
// if no such row exists.
func (r *Repo) Delete(ctx context.Context, id string) error {
	res, err := r.rw.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete media: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete media rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	return nil
}

// rowScanner is the common surface of *sql.Row and *sql.Rows for Scan.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanMedia(s rowScanner) (Media, error) {
	var (
		m                Media
		mediaType        string
		originalFilename sql.NullString
		timestamp        sql.NullTime
		makeN            sql.NullString
		modelN           sql.NullString
		focalLength      sql.NullString
		shutter          sql.NullString
		width            sql.NullInt64
		height           sql.NullInt64
		iso              sql.NullInt64
		aperture         sql.NullFloat64
		durationMs       sql.NullInt64
		thumbUpdatedAt   sql.NullTime
	)
	if err := s.Scan(
		&m.ID,
		&m.Owner.Hub,
		&m.Owner.UserID,
		&mediaType,
		&m.MimeType,
		&m.Path,
		&originalFilename,
		&m.ImportedAt,
		&timestamp,
		&m.Size,
		&m.Checksum,
		&makeN,
		&modelN,
		&focalLength,
		&shutter,
		&width,
		&height,
		&iso,
		&aperture,
		&durationMs,
		&m.ThumbStatus,
		&m.ThumbVersion,
		&thumbUpdatedAt,
	); err != nil {
		return Media{}, err
	}

	m.Type = Type(mediaType)
	m.OriginalFilename = originalFilename.String
	if timestamp.Valid {
		t := timestamp.Time
		m.Timestamp = &t
	}
	m.Make = makeN.String
	m.Model = modelN.String
	m.FocalLength = focalLength.String
	m.Shutter = shutter.String
	if width.Valid {
		v := int(width.Int64)
		m.Width = &v
	}
	if height.Valid {
		v := int(height.Int64)
		m.Height = &v
	}
	if iso.Valid {
		v := int(iso.Int64)
		m.ISO = &v
	}
	if aperture.Valid {
		v := aperture.Float64
		m.Aperture = &v
	}
	if durationMs.Valid {
		v := durationMs.Int64
		m.DurationMs = &v
	}
	if thumbUpdatedAt.Valid {
		t := thumbUpdatedAt.Time
		m.ThumbUpdatedAt = &t
	}
	return m, nil
}

// ScanMediaForAlbum is scanMedia re-exported for internal/album.
// internal packages are in the same module so a cross-package helper
// is fine; kept narrowly named so callers don't repurpose it.
func ScanMediaForAlbum(s interface {
	Scan(dest ...any) error
}) (Media, error) {
	return scanMedia(s)
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

func nullInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nullFloat(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

func nullTime(p *time.Time) sql.NullTime {
	if p == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *p, Valid: true}
}

// uniqueViolationKind inspects a SQLite error and returns the matching
// sentinel (ErrDuplicateChecksum or ErrDuplicatePath) when the error is
// a UNIQUE constraint violation on the media table. It returns nil for
// any other error so the caller can distinguish a real SQL failure.
func uniqueViolationKind(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, "UNIQUE constraint failed") {
		return nil
	}
	switch {
	case strings.Contains(msg, "media.checksum"):
		return ErrDuplicateChecksum
	case strings.Contains(msg, "media.path"):
		return ErrDuplicatePath
	default:
		// Unknown UNIQUE violation — fall back to the generic sentinel so
		// callers using errors.Is(errs.ErrAlreadyExists) still match.
		return errs.ErrAlreadyExists
	}
}

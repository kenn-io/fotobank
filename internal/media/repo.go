package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

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
	make, model, lens_model, focal_length, shutter, width, height, iso, aperture,
	duration_ms,
	latitude, longitude, gps_at, location_label,
	thumb_status, thumb_version, thumb_updated_at,
	import_source_path, paired_with_id,
	hidden_at
FROM media`

// mediaColumnsQualified is the m-prefixed projection used when the
// query joins a CTE that also has an `id` column. Keep column order
// identical to mediaSelect so scanMedia works unchanged. Two more
// copies of this column list live in this file as mediaInsert and
// in internal/album/repo.go as albumMediaMediaSelect; schema changes
// must sync all four.
const mediaColumnsQualified = `
    m.id, m.owner_hub, m.owner_user_id, m.media_type, m.mime_type, m.path, m.original_filename,
    m.imported_at, m.timestamp, m.size, m.checksum,
    m.make, m.model, m.lens_model, m.focal_length, m.shutter, m.width, m.height, m.iso, m.aperture,
    m.duration_ms,
    m.latitude, m.longitude, m.gps_at, m.location_label,
    m.thumb_status, m.thumb_version, m.thumb_updated_at,
    m.import_source_path, m.paired_with_id,
    m.hidden_at`

const mediaInsert = `INSERT INTO media (
	id, owner_hub, owner_user_id, media_type, mime_type, path, original_filename,
	imported_at, timestamp, size, checksum,
	make, model, lens_model, focal_length, shutter, width, height, iso, aperture,
	duration_ms,
	latitude, longitude, gps_at, location_label,
	thumb_status, thumb_version, thumb_updated_at,
	import_source_path, paired_with_id,
	hidden_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// Insert stores a new media row. Returns errs.ErrAlreadyExists (wrapped)
// if a row already exists with the same (owner, checksum) or (owner, path).
// Returns errs.ErrInvalidArgument if Latitude and Longitude are not both
// set or both nil — the GPS coordinate pair is documented as atomic on
// the Media struct.
func (r *Repo) Insert(ctx context.Context, m Media) error {
	if err := validateGPSPair(m.Latitude, m.Longitude); err != nil {
		return fmt.Errorf("insert media: %w", err)
	}
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
		nullStr(m.LensModel),
		nullStr(m.FocalLength),
		nullStr(m.Shutter),
		nullInt(m.Width),
		nullInt(m.Height),
		nullInt(m.ISO),
		nullFloat(m.Aperture),
		nullInt64(m.DurationMs),
		nullFloat(m.Latitude),
		nullFloat(m.Longitude),
		nullTime(m.GPSAt),
		nullStr(m.LocationLabel),
		m.ThumbStatus,
		m.ThumbVersion,
		nullTime(m.ThumbUpdatedAt),
		m.ImportSourcePath,
		pairedWithIDArg(m.PairedWithID),
		nullTime(m.HiddenAt),
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

// GetByIDs returns media rows in the same order as ids. Returns
// errs.ErrNotFound (wrapped) listing the missing ids if any id can't
// be resolved. Empty input returns (nil, nil) without querying.
//
// Internally chunks ids in fixed-size batches (250 ids → 500 bind
// vars) to stay well under SQLite's default 999-variable limit so a
// large batch (e.g. the post-import pair pass loading every just-
// imported row) doesn't trip the limit.
//
// Order preservation uses a VALUES-CTE carrying the caller-supplied
// position; the CTE's `id` column collides with media.id so the SELECT
// uses the m-prefixed projection.
func (r *Repo) GetByIDs(ctx context.Context, ids []string) ([]Media, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	const chunkSize = 250
	out := make([]Media, 0, len(ids))
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		chunk := ids[start:end]
		valRows := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*2)
		for i, id := range chunk {
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
		for rows.Next() {
			m, err := scanMedia(rows)
			if err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan media by id: %w", err)
			}
			out = append(out, m)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iter media by ids: %w", err)
		}
		_ = rows.Close()
	}
	if len(out) != len(ids) {
		found := make(map[string]struct{}, len(out))
		for _, m := range out {
			found[m.ID] = struct{}{}
		}
		var missing []string
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				missing = append(missing, id)
			}
		}
		return nil, fmt.Errorf("get media by ids: missing %d row(s): %v: %w",
			len(missing), missing, errs.ErrNotFound)
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
	if !f.IncludeSidecars {
		conds = append(conds, "paired_with_id IS NULL")
	}
	if !f.IncludeHidden {
		conds = append(conds, "hidden_at IS NULL")
	}
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
	defer func() { _ = rows.Close() }()

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
// IncludeSidecars and IncludeHidden are both set so reconcile and pairing
// backfill callers see every row regardless of pair or hidden status.
func (r *Repo) ListAll(ctx context.Context, owner owners.Principal) ([]Media, error) {
	var out []Media
	offset := 0
	for {
		page, err := r.List(ctx, ListFilter{
			Owner:           owner,
			Limit:           defaultListLimit,
			Offset:          offset,
			IncludeSidecars: true, // reconcile + bulk callers see every row
			IncludeHidden:   true, // hidden rows must not be invisible to bulk ops
		})
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

// validateGPSPair enforces the atomic-pair invariant on the GPS
// coordinates: either both lat and lon are set, or both are nil. Partial
// state would leak through Repo.Insert / UpdateGPS into rows that the
// FillMissing and Relabel candidate predicates both exclude (each
// requires BOTH NULL or BOTH NOT NULL), making them un-fixable via the
// backfill CLI. Returns errs.ErrInvalidArgument on violation.
func validateGPSPair(lat, lon *float64) error {
	if (lat == nil) != (lon == nil) {
		return fmt.Errorf("%w: latitude and longitude must both be set or both be nil",
			errs.ErrInvalidArgument)
	}
	return nil
}

// GPSBackfillMode discriminates what `gps backfill` considers a target.
// See spec §4.7 / §7.4.
type GPSBackfillMode int

const (
	// GPSBackfillModeFull selects every photo row regardless of GPS state.
	GPSBackfillModeFull GPSBackfillMode = iota
	// GPSBackfillModeFillMissing selects only photo rows where BOTH
	// latitude AND longitude are NULL. Rows with one coord set are
	// partial state from a prior run and are intentionally not targets
	// of fill-missing.
	GPSBackfillModeFillMissing
	// GPSBackfillModeRelabel selects only photo rows where lat AND lon
	// are NOT NULL. Used to refresh location_label after the embedded
	// gazetteer is bumped.
	GPSBackfillModeRelabel
)

// UpdateGPS sets the four GPS columns on an existing row. Used by the
// backfill CLI; the importer uses Insert. Returns errs.ErrNotFound if
// the row is gone, or errs.ErrInvalidArgument if exactly one of lat/lon
// is set (the pair is atomic — both set or both nil).
func (r *Repo) UpdateGPS(
	ctx context.Context,
	id string,
	lat, lon *float64,
	gpsAt *time.Time,
	label string,
) error {
	if err := validateGPSPair(lat, lon); err != nil {
		return fmt.Errorf("update media gps: %w", err)
	}
	res, err := r.rw.ExecContext(ctx,
		`UPDATE media
		    SET latitude = ?, longitude = ?, gps_at = ?, location_label = ?
		  WHERE id = ?`,
		nullFloat(lat),
		nullFloat(lon),
		nullTime(gpsAt),
		nullStr(label),
		id,
	)
	if err != nil {
		return fmt.Errorf("update media gps: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update media gps rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	return nil
}

// ListGPSBackfillCandidates enumerates rows for `gps backfill`. Always
// excludes media_type='video' (video GPS is out of scope per spec §5.5).
// Rows are ordered by id and paginated via a keyset cursor: pass
// afterID="" for the first page, then the last returned row's ID for
// subsequent pages. Keyset (rather than offset) is required because the
// backfill loop mutates rows that may or may not stay in the candidate
// set after update — offset would either skip rows (FillMissing) or
// loop forever (Full/Relabel).
//
// `since` filters by imported_at >= *since; pass nil to disable.
func (r *Repo) ListGPSBackfillCandidates(
	ctx context.Context,
	owner owners.Principal,
	mode GPSBackfillMode,
	since *time.Time,
	afterID string,
	limit int,
) ([]Media, error) {
	conds := []string{
		"owner_hub = ?",
		"owner_user_id = ?",
		"media_type = ?",
	}
	args := []any{owner.Hub, owner.UserID, string(TypePhoto)}

	switch mode {
	case GPSBackfillModeFull:
		// no GPS predicate
	case GPSBackfillModeFillMissing:
		conds = append(conds, "latitude IS NULL AND longitude IS NULL")
	case GPSBackfillModeRelabel:
		conds = append(conds, "latitude IS NOT NULL AND longitude IS NOT NULL")
	default:
		return nil, fmt.Errorf("%w: unknown GPSBackfillMode %d", errs.ErrInvalidArgument, mode)
	}
	if since != nil {
		conds = append(conds, "imported_at >= ?")
		args = append(args, *since)
	}
	if afterID != "" {
		conds = append(conds, "id > ?")
		args = append(args, afterID)
	}

	if limit <= 0 {
		limit = defaultListLimit
	}

	query := mediaSelect +
		" WHERE " + strings.Join(conds, " AND ") +
		" ORDER BY id LIMIT ?"
	args = append(args, limit)

	rows, err := r.ro.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list gps backfill candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan gps backfill candidate: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate gps backfill candidates: %w", err)
	}
	return out, nil
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
		lensModel        sql.NullString
		focalLength      sql.NullString
		shutter          sql.NullString
		width            sql.NullInt64
		height           sql.NullInt64
		iso              sql.NullInt64
		aperture         sql.NullFloat64
		durationMs       sql.NullInt64
		latitude         sql.NullFloat64
		longitude        sql.NullFloat64
		gpsAt            sql.NullTime
		locationLabel    sql.NullString
		thumbUpdatedAt   sql.NullTime
		importSourcePath sql.NullString
		pairedWithID     sql.NullString
		hiddenAt         sql.NullTime
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
		&lensModel,
		&focalLength,
		&shutter,
		&width,
		&height,
		&iso,
		&aperture,
		&durationMs,
		&latitude,
		&longitude,
		&gpsAt,
		&locationLabel,
		&m.ThumbStatus,
		&m.ThumbVersion,
		&thumbUpdatedAt,
		&importSourcePath,
		&pairedWithID,
		&hiddenAt,
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
	m.LensModel = lensModel.String
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
	if latitude.Valid {
		v := latitude.Float64
		m.Latitude = &v
	}
	if longitude.Valid {
		v := longitude.Float64
		m.Longitude = &v
	}
	if gpsAt.Valid {
		t := gpsAt.Time
		m.GPSAt = &t
	}
	m.LocationLabel = locationLabel.String
	if thumbUpdatedAt.Valid {
		t := thumbUpdatedAt.Time
		m.ThumbUpdatedAt = &t
	}
	m.ImportSourcePath = importSourcePath.String
	if pairedWithID.Valid {
		v := pairedWithID.String
		m.PairedWithID = &v
	}
	if hiddenAt.Valid {
		t := hiddenAt.Time
		m.HiddenAt = &t
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

// pairedWithIDArg yields a driver-friendly NULL for a nil pointer and
// the dereferenced string otherwise. paired_with_id is a nullable FK
// to media.id; the empty-string-as-NULL coercion that nullStr applies
// is wrong here because "" is not a valid id but is a meaningful empty
// value for other text columns.
func pairedWithIDArg(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// ListByOwnerDirectories returns rows for owner whose
// dir(import_source_path) is in dirs. Used by the F2.2 pairing pass
// to fetch existing rows in directories touched by the just-imported
// batch. Empty dirs returns nil. Rows with empty import_source_path
// are excluded.
//
// Directory keys are NFC-normalized on both sides of the comparison
// so a caller passing NFC dirs matches rows stored as NFD (e.g. macOS
// filesystem-sourced paths) and vice versa.
func (r *Repo) ListByOwnerDirectories(
	ctx context.Context,
	owner owners.Principal,
	dirs []string,
) ([]Media, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	dirSet := make(map[string]struct{}, len(dirs))
	for _, d := range dirs {
		dirSet[norm.NFC.String(d)] = struct{}{}
	}
	q := mediaSelect + `
WHERE owner_hub = ? AND owner_user_id = ?
  AND import_source_path != ''`
	rows, err := r.ro.QueryContext(ctx, q, owner.Hub, owner.UserID)
	if err != nil {
		return nil, fmt.Errorf("list by owner directories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Media, 0)
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan media: %w", err)
		}
		if _, ok := dirSet[norm.NFC.String(filepath.Dir(m.ImportSourcePath))]; !ok {
			continue
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media: %w", err)
	}
	return out, nil
}

// UpdateLensModelIfNull writes lens_model for the given media row,
// but only when the existing column is NULL. The reconcile backfill
// uses this to fill in lens_model for rows imported before the column
// existed without overwriting any value already present. Returns
// (true, nil) when the row was updated, (false, nil) when no row
// changed (either the id was unknown or lens_model was already set),
// and a wrapped error on any DB failure.
func (r *Repo) UpdateLensModelIfNull(ctx context.Context, id, lensModel string) (bool, error) {
	if lensModel == "" {
		return false, nil
	}
	res, err := r.rw.ExecContext(ctx,
		`UPDATE media SET lens_model = ? WHERE id = ? AND lens_model IS NULL`,
		lensModel, id,
	)
	if err != nil {
		return false, fmt.Errorf("update lens_model: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update lens_model rows affected: %w", err)
	}
	return n == 1, nil
}

// UpdatePairedWithID writes the paired_with_id column for a single
// row. nil clears the FK to NULL; non-nil sets it to the primary's
// id. The owner-consistency triggers in
// internal/db/migrations/000001_initial_schema.up.sql RAISE if the
// caller tries to point a sidecar at a primary owned by a different
// principal — this is defence in depth; the service-layer pairing
// pass already restricts candidates to one owner per (owner,
// directory) group. Returns errs.ErrNotFound if no row matches id.
func (r *Repo) UpdatePairedWithID(
	ctx context.Context,
	id string,
	primaryID *string,
) error {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE media SET paired_with_id = ? WHERE id = ?`,
		pairedWithIDArg(primaryID), id,
	)
	if err != nil {
		return fmt.Errorf("update paired_with_id: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update paired_with_id rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	return nil
}

// GetSidecars returns rows whose paired_with_id == primaryID, sorted
// by original_filename ascending with id ASC as a deterministic
// tiebreaker. Used by the HTTP detail handler to embed sidecars in
// a primary's DTO. Returns an empty slice when the primary has no
// sidecars; never returns errs.ErrNotFound for that case (an empty
// list is the legitimate result, not an error).
//
// When includeHidden is false, rows whose hidden_at IS NOT NULL are
// excluded — a hidden sidecar under a visible primary must not leak.
func (r *Repo) GetSidecars(
	ctx context.Context,
	primaryID string,
	includeHidden bool,
) ([]Media, error) {
	q := mediaSelect + `
WHERE paired_with_id = ?`
	if !includeHidden {
		q += ` AND hidden_at IS NULL`
	}
	q += `
ORDER BY COALESCE(original_filename, '') ASC, id ASC`
	rows, err := r.ro.QueryContext(ctx, q, primaryID)
	if err != nil {
		return nil, fmt.Errorf("get sidecars: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Media, 0)
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

// GetByIDVisible returns the media row with the given id. If
// includeHidden is false and the row has hidden_at set, it returns
// errs.ErrNotFound as if the row did not exist. When includeHidden is
// true the behaviour is identical to GetByID.
func (r *Repo) GetByIDVisible(ctx context.Context, id string, includeHidden bool) (Media, error) {
	m, err := r.GetByID(ctx, id)
	if err != nil {
		return Media{}, err
	}
	if !includeHidden && m.HiddenAt != nil {
		return Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	return m, nil
}

// inPlaceholders returns a string of n comma-separated "?" placeholders.
func inPlaceholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.Repeat("?,", n)[:n*2-1]
}

// SetHiddenCascade sets hidden_at = at on every owned row whose id IS in
// ids OR paired_with_id IS in ids. Sidecars cascade with their primary.
// Large id slices are chunked transparently to stay under the SQLite
// parameter limit. All chunks run inside a single transaction so a
// mid-chunk failure leaves no partial state.
func (r *Repo) SetHiddenCascade(
	ctx context.Context,
	owner owners.Principal,
	ids []string,
	at time.Time,
) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set hidden cascade: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const chunkSize = 250
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		chunk := ids[start:end]
		ph := inPlaceholders(len(chunk))
		args := make([]any, 0, 3+len(chunk)*2)
		args = append(args, at, owner.Hub, owner.UserID)
		for _, id := range chunk {
			args = append(args, id)
		}
		for _, id := range chunk {
			args = append(args, id)
		}
		q := `UPDATE media
		   SET hidden_at = ?
		 WHERE owner_hub = ? AND owner_user_id = ?
		   AND (id IN (` + ph + `) OR paired_with_id IN (` + ph + `))`
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("set hidden cascade: %w", err)
		}
	}
	return tx.Commit()
}

// ClearHiddenCascade clears hidden_at on every owned row whose id IS in
// ids OR paired_with_id IS in ids. Sidecars cascade with their primary.
// Large id slices are chunked transparently to stay under the SQLite
// parameter limit. All chunks run inside a single transaction so a
// mid-chunk failure leaves no partial state.
func (r *Repo) ClearHiddenCascade(
	ctx context.Context,
	owner owners.Principal,
	ids []string,
) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clear hidden cascade: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const chunkSize = 250
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		chunk := ids[start:end]
		ph := inPlaceholders(len(chunk))
		args := make([]any, 0, 2+len(chunk)*2)
		args = append(args, owner.Hub, owner.UserID)
		for _, id := range chunk {
			args = append(args, id)
		}
		for _, id := range chunk {
			args = append(args, id)
		}
		q := `UPDATE media
		   SET hidden_at = NULL
		 WHERE owner_hub = ? AND owner_user_id = ?
		   AND (id IN (` + ph + `) OR paired_with_id IN (` + ph + `))`
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("clear hidden cascade: %w", err)
		}
	}
	return tx.Commit()
}

// ClearAllHiddenForOwner sets hidden_at = NULL on every row the owner
// owns where hidden_at IS NOT NULL. Used by hidden.Service.Disable to
// make all media visible again after the hidden feature is disabled.
func (r *Repo) ClearAllHiddenForOwner(ctx context.Context, owner owners.Principal) error {
	q := `UPDATE media
	   SET hidden_at = NULL
	 WHERE owner_hub = ? AND owner_user_id = ?
	   AND hidden_at IS NOT NULL`
	if _, err := r.rw.ExecContext(ctx, q, owner.Hub, owner.UserID); err != nil {
		return fmt.Errorf("clear all hidden for owner: %w", err)
	}
	return nil
}

// ListHidden returns primary and standalone hidden rows for owner,
// paginated by limit/offset. Sidecars (paired_with_id IS NOT NULL) are
// suppressed — they cascade with their primary so surfacing them
// separately would be redundant.
//
// Sort order: timestamp IS NULL ASC, timestamp DESC, imported_at DESC,
// id DESC. Rows with a timestamp sort before null-timestamp rows;
// among rows with a timestamp the most recent appears first.
func (r *Repo) ListHidden(
	ctx context.Context,
	owner owners.Principal,
	limit, offset int,
) ([]Media, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if offset < 0 {
		offset = 0
	}
	q := mediaSelect + `
 WHERE owner_hub = ? AND owner_user_id = ?
   AND hidden_at IS NOT NULL
   AND paired_with_id IS NULL
 ORDER BY timestamp IS NULL ASC, timestamp DESC, imported_at DESC, id DESC
 LIMIT ? OFFSET ?`
	rows, err := r.ro.QueryContext(ctx, q, owner.Hub, owner.UserID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list hidden: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan hidden: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hidden: %w", err)
	}
	return out, nil
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

package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// Repo is a SQLite-backed store of ready asset projections. It uses a split
// read/write pool: writes go through rw and reads through ro.
type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewRepo constructs a Repo. rw must be the writer pool and ro the reader pool.
func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

const mediaSelect = `SELECT
	a.id, a.owner_hub, a.owner_user_id, a.media_type,
	f.id, f.mime_type, f.original_filename, a.imported_at, a.timestamp,
	f.size, f.sha256, f.current_version_id, f.docbank_virtual_path,
	a.make, a.model, a.lens_model, a.focal_length, a.shutter,
	a.width, a.height, a.iso, a.aperture, a.duration_ms,
	a.latitude, a.longitude, a.gps_at, a.location_label,
	a.thumb_status, a.thumb_version, a.thumb_updated_at, a.hidden_at
FROM assets a
JOIN media_files f ON f.asset_id = a.id AND f.role = 'primary'`

// mediaColumnsQualified is the m-prefixed projection used when the
// query joins a CTE that also has an `id` column. Keep column order
// identical to mediaSelect so scanMedia works unchanged. Two more
// copies of this column list live in this file as mediaInsert and
// in internal/album/repo.go as albumMediaMediaSelect; schema changes
// must sync all four.
const mediaColumnsQualified = `
    a.id, a.owner_hub, a.owner_user_id, a.media_type,
    f.id, f.mime_type, f.original_filename, a.imported_at, a.timestamp,
    f.size, f.sha256, f.current_version_id, f.docbank_virtual_path,
    a.make, a.model, a.lens_model, a.focal_length, a.shutter,
    a.width, a.height, a.iso, a.aperture, a.duration_ms,
    a.latitude, a.longitude, a.gps_at, a.location_label,
    a.thumb_status, a.thumb_version, a.thumb_updated_at, a.hidden_at`

// WithWriteTx runs fn inside a transaction on the writer pool. The
// closure can call any of the repo's *Tx methods (Insert is not yet
// tx-aware) and combine them with cross-package writes (e.g. an FTS
// refresh in internal/search/index). Commits when fn returns nil;
// rolls back otherwise.
//
// This is the public escape hatch around the repo's private writer
// handle so callers in other packages can compose write transactions
// without forcing the repo to import their packages.
func (r *Repo) WithWriteTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("media repo: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("media repo: commit: %w", err)
	}
	return nil
}

// GetByID returns the media row with the given id. Returns errs.ErrNotFound
// if no such row exists.
func (r *Repo) GetByID(ctx context.Context, id string) (Media, error) {
	row := r.ro.QueryRowContext(ctx, mediaSelect+" WHERE a.id = ? AND a.state = 'ready'", id)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
	}
	if err != nil {
		return Media{}, fmt.Errorf("get media: %w", err)
	}
	return m, nil
}

// GetByOwnerSHA256 returns the ready asset whose primary file has the given
// content identity. Returns
// errs.ErrNotFound if no such row exists.
func (r *Repo) GetByOwnerSHA256(ctx context.Context, p owners.Principal, sha256 string) (Media, error) {
	row := r.ro.QueryRowContext(ctx,
		mediaSelect+" WHERE a.owner_hub = ? AND a.owner_user_id = ? AND f.sha256 = ? AND a.state = 'ready'",
		p.Hub, p.UserID, sha256,
	)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, fmt.Errorf("%w: media owner=%s sha256=%s", errs.ErrNotFound, p, sha256)
	}
	if err != nil {
		return Media{}, fmt.Errorf("get media by SHA-256: %w", err)
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
  FROM assets a
  JOIN media_files f ON f.asset_id = a.id AND f.role = 'primary'
  JOIN ord ON ord.id = a.id
 WHERE a.state = 'ready'
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
	conds = append(conds, "a.owner_hub = ?", "a.owner_user_id = ?", "a.state = 'ready'")
	args = append(args, f.Owner.Hub, f.Owner.UserID)
	if !f.IncludeHidden {
		conds = append(conds, "a.hidden_at IS NULL")
	}
	if f.DateFrom != nil {
		conds = append(conds, "a.timestamp >= ?")
		args = append(args, *f.DateFrom)
	}
	if f.DateTo != nil {
		conds = append(conds, "a.timestamp < ?")
		args = append(args, *f.DateTo)
	}
	appendFacetConds(&conds, &args, f.Type, f.Cameras, f.Lenses, f.AnyTagKeys)
	if f.HasGPS != nil {
		if *f.HasGPS {
			conds = append(conds, "a.latitude IS NOT NULL AND a.longitude IS NOT NULL")
		} else {
			conds = append(conds, "(a.latitude IS NULL OR a.longitude IS NULL)")
		}
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
		" ORDER BY a.timestamp " + direction + " NULLS LAST, a.imported_at " + direction + ", a.id " + direction +
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

// ListAll returns every ready asset for an owner, including hidden assets,
// in pages of defaultListLimit. User-facing queries should use List with an
// explicit limit.
func (r *Repo) ListAll(ctx context.Context, owner owners.Principal) ([]Media, error) {
	var out []Media
	offset := 0
	for {
		page, err := r.List(ctx, ListFilter{
			Owner:         owner,
			Limit:         defaultListLimit,
			Offset:        offset,
			IncludeHidden: true,
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
	res, err := r.rw.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id)
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

// UpdateGPSTx changes the four GPS fields inside the caller's transaction so
// the update can be bundled with a media_fts refresh
// (location_label is part of the FTS corpus). The update succeeds only while
// expectedVersionID remains the asset's current primary content version.
func (r *Repo) UpdateGPSTx(
	ctx context.Context,
	tx *sql.Tx,
	id string,
	expectedVersionID string,
	lat, lon *float64,
	gpsAt *time.Time,
	label string,
) error {
	if err := validateOpaqueUUID(expectedVersionID, "expected content version ID"); err != nil {
		return fmt.Errorf("update media gps: %w", err)
	}
	if err := validateGPSPair(lat, lon); err != nil {
		return fmt.Errorf("update media gps: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE assets
		    SET latitude = ?, longitude = ?, gps_at = ?, location_label = ?
		  WHERE id = ? AND EXISTS (
		    SELECT 1 FROM media_files
		    WHERE asset_id = assets.id AND role = 'primary' AND current_version_id = ?
		  )`,
		nullFloat(lat),
		nullFloat(lon),
		nullTime(gpsAt),
		nullStr(label),
		id,
		expectedVersionID,
	)
	if err != nil {
		return fmt.Errorf("update media gps: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update media gps rows affected: %w", err)
	}
	if n == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("update media gps: inspect asset: %w", err)
		}
		if exists == 0 {
			return fmt.Errorf("%w: media id=%s", errs.ErrNotFound, id)
		}
		return fmt.Errorf("%w: primary content version changed", errs.ErrContentConflict)
	}
	return nil
}

// ListGPSBackfillCandidates enumerates rows for `gps backfill`. It always
// excludes media_type='video' because video GPS extraction is not supported.
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
		"a.owner_hub = ?",
		"a.owner_user_id = ?",
		"a.media_type = ?",
		"a.state = 'ready'",
	}
	args := []any{owner.Hub, owner.UserID, string(TypePhoto)}

	switch mode {
	case GPSBackfillModeFull:
		// no GPS predicate
	case GPSBackfillModeFillMissing:
		conds = append(conds, "a.latitude IS NULL AND a.longitude IS NULL")
	case GPSBackfillModeRelabel:
		conds = append(conds, "a.latitude IS NOT NULL AND a.longitude IS NOT NULL")
	default:
		return nil, fmt.Errorf("%w: unknown GPSBackfillMode %d", errs.ErrInvalidArgument, mode)
	}
	if since != nil {
		conds = append(conds, "a.imported_at >= ?")
		args = append(args, *since)
	}
	if afterID != "" {
		conds = append(conds, "a.id > ?")
		args = append(args, afterID)
	}

	if limit <= 0 {
		limit = defaultListLimit
	}

	query := mediaSelect +
		" WHERE " + strings.Join(conds, " AND ") +
		" ORDER BY a.id LIMIT ?"
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
		m              Media
		mediaType      string
		timestamp      sql.NullTime
		makeN          sql.NullString
		modelN         sql.NullString
		lensModel      sql.NullString
		focalLength    sql.NullString
		shutter        sql.NullString
		width          sql.NullInt64
		height         sql.NullInt64
		iso            sql.NullInt64
		aperture       sql.NullFloat64
		durationMs     sql.NullInt64
		latitude       sql.NullFloat64
		longitude      sql.NullFloat64
		gpsAt          sql.NullTime
		locationLabel  sql.NullString
		thumbUpdatedAt sql.NullTime
		hiddenAt       sql.NullTime
	)
	if err := s.Scan(
		&m.ID,
		&m.Owner.Hub,
		&m.Owner.UserID,
		&mediaType,
		&m.PrimaryFileID,
		&m.MimeType,
		&m.OriginalFilename,
		&m.ImportedAt,
		&timestamp,
		&m.Size,
		&m.SHA256,
		&m.CurrentVersionID,
		&m.DocbankVirtualPath,
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
		&hiddenAt,
	); err != nil {
		return Media{}, err
	}

	m.Type = Type(mediaType)
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

// UpdateLensModelIfNull writes lens_model for the given asset,
// but only when the existing column is NULL. Returns
// (true, nil) when the row was updated, (false, nil) when no row
// changed (either the id was unknown or lens_model was already set),
// and a wrapped error on any DB failure.
//
// This is the auto-commit wrapper around UpdateLensModelIfNullTx;
// callers that need to bundle the update with a media_fts refresh
// (lens_model is part of the FTS corpus) should use the Tx variant.
func (r *Repo) UpdateLensModelIfNull(ctx context.Context, id, lensModel string) (bool, error) {
	if lensModel == "" {
		return false, nil
	}
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("update lens_model: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	updated, err := r.UpdateLensModelIfNullTx(ctx, tx, id, lensModel)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("update lens_model: commit: %w", err)
	}
	return updated, nil
}

// UpdateLensModelIfNullTx is the in-tx variant of UpdateLensModelIfNull.
// The caller owns the transaction so the conditional UPDATE can be
// bundled with a media_fts refresh (lens_model is part of the FTS
// corpus). The empty-lensModel guard mirrors the auto-commit wrapper
// to keep behaviour identical.
func (r *Repo) UpdateLensModelIfNullTx(
	ctx context.Context,
	tx *sql.Tx,
	id, lensModel string,
) (bool, error) {
	if lensModel == "" {
		return false, nil
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE assets SET lens_model = ? WHERE id = ? AND lens_model IS NULL`,
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

// ListFiles returns every file in an asset graph, ordered by role and ID.
func (r *Repo) ListFiles(ctx context.Context, assetID string) ([]File, error) {
	return NewAssetRepo(r.rw, r.ro).ListFiles(ctx, assetID)
}

// GetFile returns one file by its opaque ID.
func (r *Repo) GetFile(ctx context.Context, fileID string) (File, error) {
	return NewAssetRepo(r.rw, r.ro).GetFile(ctx, fileID)
}

// inPlaceholders returns a string of n comma-separated "?" placeholders.
func inPlaceholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.Repeat("?,", n)[:n*2-1]
}

// placeholders returns "?, ?, ..., ?" with n question marks. Used by
// the repo-level IN-list cond emitters; returns "" when n <= 0.
// Mirrors the helper in internal/search/hybrid/filter.go — keep both
// in sync.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := strings.Repeat("?, ", n)
	return out[:len(out)-2]
}

// appendFacetConds emits the Type/Cameras/Lenses/AnyTagKeys conds shared
// by Repo.List, Repo.ListGeo, and the FacetService aggregator. The
// hidden, sidecar, GPS, and date-range conds stay caller-specific
// because not every consumer wants them. Accumulators are mutated in
// place to match the surrounding append-style call sites.
func appendFacetConds(
	conds *[]string, args *[]any,
	mediaType *Type, cameras, lenses, anyTagKeys []string,
) {
	if mediaType != nil {
		*conds = append(*conds, "a.media_type = ?")
		*args = append(*args, string(*mediaType))
	}
	if len(cameras) > 0 {
		*conds = append(*conds,
			"(a.make || ' ' || a.model) IN ("+placeholders(len(cameras))+")")
		for _, v := range cameras {
			*args = append(*args, v)
		}
	}
	if len(lenses) > 0 {
		*conds = append(*conds, "a.lens_model IN ("+placeholders(len(lenses))+")")
		for _, v := range lenses {
			*args = append(*args, v)
		}
	}
	if len(anyTagKeys) > 0 {
		*conds = append(*conds,
			`EXISTS (SELECT 1 FROM media_tags mt
                      JOIN ai_results r ON mt.result_id = r.id
                     WHERE r.media_id = a.id AND r.task = 'tag' AND r.status = 'active'
                       AND mt.tag_key IN (`+placeholders(len(anyTagKeys))+`))`)
		for _, v := range anyTagKeys {
			*args = append(*args, v)
		}
	}
}

// SetHidden sets hidden_at on each owned asset ID.
// Large id slices are chunked transparently to stay under the SQLite
// parameter limit. All chunks run inside a single transaction so a
// mid-chunk failure leaves no partial state.
func (r *Repo) SetHidden(
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
		return fmt.Errorf("set hidden: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const chunkSize = 250
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		chunk := ids[start:end]
		ph := inPlaceholders(len(chunk))
		args := make([]any, 0, 3+len(chunk))
		args = append(args, at, owner.Hub, owner.UserID)
		for _, id := range chunk {
			args = append(args, id)
		}
		q := `UPDATE assets
		   SET hidden_at = ?
		 WHERE owner_hub = ? AND owner_user_id = ?
		   AND id IN (` + ph + `)`
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("set hidden: %w", err)
		}
	}
	return tx.Commit()
}

// ClearHidden clears hidden_at on each owned asset ID.
// Large id slices are chunked transparently to stay under the SQLite
// parameter limit. All chunks run inside a single transaction so a
// mid-chunk failure leaves no partial state.
func (r *Repo) ClearHidden(
	ctx context.Context,
	owner owners.Principal,
	ids []string,
) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clear hidden: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const chunkSize = 250
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		chunk := ids[start:end]
		ph := inPlaceholders(len(chunk))
		args := make([]any, 0, 2+len(chunk))
		args = append(args, owner.Hub, owner.UserID)
		for _, id := range chunk {
			args = append(args, id)
		}
		q := `UPDATE assets
		   SET hidden_at = NULL
		 WHERE owner_hub = ? AND owner_user_id = ?
		   AND id IN (` + ph + `)`
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("clear hidden: %w", err)
		}
	}
	return tx.Commit()
}

// ClearAllHiddenForOwner sets hidden_at = NULL on every row the owner
// owns where hidden_at IS NOT NULL. Used by hidden.Service.Disable to
// make all media visible again after the hidden feature is disabled.
func (r *Repo) ClearAllHiddenForOwner(ctx context.Context, owner owners.Principal) error {
	q := `UPDATE assets
	   SET hidden_at = NULL
	 WHERE owner_hub = ? AND owner_user_id = ?
	   AND hidden_at IS NOT NULL`
	if _, err := r.rw.ExecContext(ctx, q, owner.Hub, owner.UserID); err != nil {
		return fmt.Errorf("clear all hidden for owner: %w", err)
	}
	return nil
}

// ListHidden returns hidden ready assets for owner, paginated by limit/offset.
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
 WHERE a.owner_hub = ? AND a.owner_user_id = ?
   AND a.state = 'ready' AND a.hidden_at IS NOT NULL
 ORDER BY a.timestamp IS NULL ASC, a.timestamp DESC, a.imported_at DESC, a.id DESC
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

// ListGeo returns ready assets for owner that have GPS coordinates. Rows
// missing either latitude or longitude are excluded. When IncludeHidden is
// false (default), hidden rows are also excluded; when true, all rows
// — visible and hidden — are returned (the handler is expected to
// have validated an unlock claim before calling).
//
// Sort order: timestamp DESC NULLS LAST, imported_at DESC, id DESC.
// The SQL itself uses the SQLite-portable `IS NULL ASC, timestamp DESC`
// idiom matching ListHidden so rows with a non-NULL timestamp appear
// before NULL-timestamp rows.
func (r *Repo) ListGeo(ctx context.Context, f ListGeoFilter) ([]Media, error) {
	conds := []string{
		"a.owner_hub = ?", "a.owner_user_id = ?", "a.state = 'ready'",
		"a.latitude IS NOT NULL", "a.longitude IS NOT NULL",
	}
	args := []any{f.Owner.Hub, f.Owner.UserID}

	if !f.IncludeHidden {
		conds = append(conds, "a.hidden_at IS NULL")
	}
	appendFacetConds(&conds, &args, f.Type, f.Cameras, f.Lenses, f.AnyTagKeys)

	q := mediaSelect + " WHERE " + strings.Join(conds, " AND ") +
		" ORDER BY a.timestamp IS NULL ASC, a.timestamp DESC, a.imported_at DESC, a.id DESC"
	rows, err := r.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list geo: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Media, 0)
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan geo: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate geo: %w", err)
	}
	return out, nil
}

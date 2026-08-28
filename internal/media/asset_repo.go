package media

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// AssetRepo stores complete asset/file graphs. It uses a split read/write
// pool: writes go through rw and reads through ro.
type AssetRepo struct {
	rw *sql.DB
	ro *sql.DB
}

// NewAssetRepo constructs an AssetRepo.
func NewAssetRepo(rw, ro *sql.DB) *AssetRepo { return &AssetRepo{rw: rw, ro: ro} }

const assetInsert = `INSERT INTO assets (
	id, owner_hub, owner_user_id, state, media_type, imported_at, timestamp,
	make, model, lens_model, focal_length, shutter, width, height, iso, aperture,
	duration_ms, latitude, longitude, gps_at, location_label,
	thumb_status, thumb_version, thumb_updated_at, hidden_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const fileInsert = `INSERT INTO media_files (
	id, asset_id, owner_hub, owner_user_id, role, mime_type,
	original_filename, import_source_path, size, docbank_node_id,
	docbank_virtual_path, current_version_id, sha256
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const relationshipInsert = `INSERT INTO media_file_relationships (
	source_file_id, target_file_id, kind
) VALUES (?, ?, ?)`

const operationInsert = `INSERT INTO content_operations (
	id, asset_id, file_id, owner_hub, owner_user_id, status,
	expected_sha256, expected_size, docbank_virtual_path,
	created_at, updated_at
) VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?)`

const assetSelect = `SELECT
	id, owner_hub, owner_user_id, state, media_type, imported_at, timestamp,
	make, model, lens_model, focal_length, shutter, width, height, iso, aperture,
	duration_ms, latitude, longitude, gps_at, location_label,
	thumb_status, thumb_version, thumb_updated_at, hidden_at
FROM assets`

const fileSelect = `SELECT
	id, asset_id, owner_hub, owner_user_id, role, mime_type,
	original_filename, import_source_path, size, docbank_node_id,
	docbank_virtual_path, current_version_id, sha256
FROM media_files`

// InsertGraph stores an asset and all its files and relationships atomically.
// Ready graphs must have exactly one primary and complete Docbank mappings.
func (r *AssetRepo) InsertGraph(
	ctx context.Context,
	asset Asset,
	files []File,
	relationships []FileRelationship,
) error {
	if err := validateAssetGraphInput(asset, files, relationships); err != nil {
		return fmt.Errorf("insert asset graph: %w", err)
	}

	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("insert asset graph: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var ownerStorageKey string
	err = tx.QueryRowContext(ctx,
		`SELECT storage_key FROM owners WHERE hub = ? AND user_id = ?`,
		asset.Owner.Hub, asset.Owner.UserID,
	).Scan(&ownerStorageKey)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("insert asset graph: %w: owner %s", errs.ErrNotFound, asset.Owner)
	}
	if err != nil {
		return fmt.Errorf("insert asset graph: read owner storage key: %w", err)
	}

	for _, file := range files {
		if err := validateDocbankMapping(file, ownerStorageKey); err != nil {
			return fmt.Errorf("insert asset graph: file %s: %w", file.ID, err)
		}
		if asset.State == AssetReady && file.DocbankNodeID == nil {
			return fmt.Errorf("insert asset graph: file %s: %w: ready asset requires mapped files",
				file.ID, errs.ErrInvalidArgument)
		}
	}

	requestedState := asset.State
	if _, err := tx.ExecContext(ctx, assetInsert,
		asset.ID,
		asset.Owner.Hub,
		asset.Owner.UserID,
		string(AssetPending),
		string(asset.Type),
		asset.ImportedAt,
		nullTime(asset.Timestamp),
		nullStr(asset.Make),
		nullStr(asset.Model),
		nullStr(asset.LensModel),
		nullStr(asset.FocalLength),
		nullStr(asset.Shutter),
		nullInt(asset.Width),
		nullInt(asset.Height),
		nullInt(asset.ISO),
		nullFloat(asset.Aperture),
		nullInt64(asset.DurationMs),
		nullFloat(asset.Latitude),
		nullFloat(asset.Longitude),
		nullTime(asset.GPSAt),
		nullStr(asset.LocationLabel),
		asset.ThumbStatus,
		asset.ThumbVersion,
		nullTime(asset.ThumbUpdatedAt),
		nullTime(asset.HiddenAt),
	); err != nil {
		return fmt.Errorf("insert asset graph: insert asset: %w", err)
	}

	fileStmt, err := tx.PrepareContext(ctx, fileInsert)
	if err != nil {
		return fmt.Errorf("insert asset graph: prepare file insert: %w", err)
	}
	defer fileStmt.Close()
	for _, file := range files {
		if _, err := fileStmt.ExecContext(ctx,
			file.ID,
			file.AssetID,
			file.Owner.Hub,
			file.Owner.UserID,
			string(file.Role),
			file.MimeType,
			file.OriginalFilename,
			file.ImportSourcePath,
			file.Size,
			nullInt64(file.DocbankNodeID),
			nullStr(file.DocbankVirtualPath),
			nullStr(file.CurrentVersionID),
			nullStr(file.SHA256),
		); err != nil {
			return fmt.Errorf("insert asset graph: insert file %s: %w", file.ID, err)
		}
	}

	relationshipStmt, err := tx.PrepareContext(ctx, relationshipInsert)
	if err != nil {
		return fmt.Errorf("insert asset graph: prepare relationship insert: %w", err)
	}
	defer relationshipStmt.Close()
	for _, relationship := range relationships {
		if _, err := relationshipStmt.ExecContext(ctx,
			relationship.SourceFileID,
			relationship.TargetFileID,
			string(relationship.Kind),
		); err != nil {
			return fmt.Errorf("insert asset graph: insert relationship: %w", err)
		}
	}

	if requestedState != AssetPending {
		if _, err := tx.ExecContext(ctx,
			`UPDATE assets SET state = ? WHERE id = ?`,
			string(requestedState), asset.ID,
		); err != nil {
			return fmt.Errorf("insert asset graph: finalize state: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert asset graph: commit: %w", err)
	}
	return nil
}

// ReserveImport atomically records a pending asset graph and one durable
// Docbank operation per file. No Docbank call may happen before this succeeds.
func (r *AssetRepo) ReserveImport(
	ctx context.Context,
	asset Asset,
	pending []PendingContent,
	relationships []FileRelationship,
) error {
	asset.State = AssetPending
	files := make([]File, len(pending))
	for i := range pending {
		files[i] = pending[i].File
		files[i].DocbankNodeID = nil
		files[i].DocbankVirtualPath = ""
		files[i].CurrentVersionID = ""
		files[i].SHA256 = ""
	}
	if err := validateAssetGraphInput(asset, files, relationships); err != nil {
		return fmt.Errorf("reserve import: %w", err)
	}
	if len(pending) == 0 {
		return fmt.Errorf("reserve import: %w: asset has no files", errs.ErrInvalidArgument)
	}

	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reserve import: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var ownerStorageKey string
	if err := tx.QueryRowContext(ctx,
		`SELECT storage_key FROM owners WHERE hub = ? AND user_id = ?`,
		asset.Owner.Hub, asset.Owner.UserID,
	).Scan(&ownerStorageKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reserve import: %w: owner %s", errs.ErrNotFound, asset.Owner)
		}
		return fmt.Errorf("reserve import: read owner storage key: %w", err)
	}

	if _, err := tx.ExecContext(ctx, assetInsert,
		asset.ID, asset.Owner.Hub, asset.Owner.UserID, string(AssetPending),
		string(asset.Type), asset.ImportedAt, nullTime(asset.Timestamp),
		nullStr(asset.Make), nullStr(asset.Model), nullStr(asset.LensModel),
		nullStr(asset.FocalLength), nullStr(asset.Shutter), nullInt(asset.Width),
		nullInt(asset.Height), nullInt(asset.ISO), nullFloat(asset.Aperture),
		nullInt64(asset.DurationMs), nullFloat(asset.Latitude),
		nullFloat(asset.Longitude), nullTime(asset.GPSAt), nullStr(asset.LocationLabel),
		asset.ThumbStatus, asset.ThumbVersion, nullTime(asset.ThumbUpdatedAt),
		nullTime(asset.HiddenAt),
	); err != nil {
		return fmt.Errorf("reserve import: insert asset: %w", err)
	}

	now := time.Now().UTC()
	for i := range pending {
		p := pending[i]
		if err := validateOpaqueUUID(p.OperationID, "operation ID"); err != nil {
			return fmt.Errorf("reserve import: %w", err)
		}
		if p.Size != p.File.Size || p.Size < 0 {
			return fmt.Errorf("reserve import: %w: file size mismatch", errs.ErrInvalidArgument)
		}
		expectedPath, pathErr := content.VirtualPath(ownerStorageKey, p.File.ID, p.File.OriginalFilename)
		if pathErr != nil || p.VirtualPath != expectedPath {
			return fmt.Errorf("reserve import: %w: invalid virtual path", errs.ErrInvalidArgument)
		}
		if !validSHA256(p.SHA256) {
			return fmt.Errorf("reserve import: %w: invalid SHA-256", errs.ErrInvalidArgument)
		}
		f := p.File
		if _, err := tx.ExecContext(ctx, fileInsert,
			f.ID, f.AssetID, f.Owner.Hub, f.Owner.UserID, string(f.Role),
			f.MimeType, f.OriginalFilename, f.ImportSourcePath, f.Size,
			nil, nil, nil, nil,
		); err != nil {
			return fmt.Errorf("reserve import: insert file %s: %w", f.ID, err)
		}
		if _, err := tx.ExecContext(ctx, operationInsert,
			p.OperationID, asset.ID, f.ID, asset.Owner.Hub, asset.Owner.UserID,
			p.SHA256, p.Size, p.VirtualPath, now, now,
		); err != nil {
			return fmt.Errorf("reserve import: insert operation %s: %w", p.OperationID, err)
		}
	}
	for _, relationship := range relationships {
		if _, err := tx.ExecContext(ctx, relationshipInsert,
			relationship.SourceFileID, relationship.TargetFileID, string(relationship.Kind),
		); err != nil {
			return fmt.Errorf("reserve import: insert relationship: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reserve import: commit: %w", err)
	}
	return nil
}

// ApplyContentReceipt atomically attaches a Docbank receipt to its file and
// settles the durable operation. The receipt must match the reserved identity.
func (r *AssetRepo) ApplyContentReceipt(ctx context.Context, receipt ContentReceipt) error {
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply content receipt: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var fileID, expectedSHA string
	var expectedSize int64
	var status string
	var existingNode sql.NullInt64
	var existingVersion sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT file_id, expected_sha256, expected_size, status,
		       docbank_node_id, docbank_version_id
		FROM content_operations WHERE id = ?`, receipt.OperationID,
	).Scan(&fileID, &expectedSHA, &expectedSize, &status, &existingNode, &existingVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("apply content receipt: %w: operation %s", errs.ErrNotFound, receipt.OperationID)
		}
		return fmt.Errorf("apply content receipt: read operation: %w", err)
	}
	if receipt.NodeID <= 0 || uuid.Validate(receipt.VersionID) != nil ||
		receipt.SHA256 != expectedSHA || receipt.Size != expectedSize {
		return fmt.Errorf("apply content receipt: %w: receipt does not match reservation", errs.ErrInvalidArgument)
	}
	if status == "applied" {
		if existingNode.Int64 == receipt.NodeID && existingVersion.String == receipt.VersionID {
			return nil
		}
		return fmt.Errorf("apply content receipt: %w: operation already has another receipt", errs.ErrContentConflict)
	}
	if status != "pending" {
		return fmt.Errorf("apply content receipt: %w: operation is terminal", errs.ErrInvalidArgument)
	}
	var virtualPath string
	if err := tx.QueryRowContext(ctx,
		`SELECT docbank_virtual_path FROM content_operations WHERE id = ?`, receipt.OperationID,
	).Scan(&virtualPath); err != nil {
		return fmt.Errorf("apply content receipt: read virtual path: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE media_files
		SET docbank_node_id = ?, docbank_virtual_path = ?, current_version_id = ?, sha256 = ?
		WHERE id = ?`, receipt.NodeID, virtualPath, receipt.VersionID, receipt.SHA256, fileID,
	); err != nil {
		return fmt.Errorf("apply content receipt: update file: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE content_operations
		SET status = 'applied', docbank_node_id = ?, docbank_version_id = ?, updated_at = ?
		WHERE id = ?`, receipt.NodeID, receipt.VersionID, time.Now().UTC(), receipt.OperationID,
	); err != nil {
		return fmt.Errorf("apply content receipt: settle operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apply content receipt: commit: %w", err)
	}
	return nil
}

// MarkContentConflict terminalizes an asset and all of its operations. A
// conflicted graph can never become ready without an explicit future resolver.
func (r *AssetRepo) MarkContentConflict(ctx context.Context, assetID string, cause error) error {
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mark content conflict: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	message := "content conflict"
	if cause != nil {
		message = cause.Error()
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE content_operations SET status = 'conflict', last_error = ?, updated_at = ?
		WHERE asset_id = ? AND status <> 'conflict'`, message, now, assetID,
	); err != nil {
		return fmt.Errorf("mark content conflict: terminalize operations: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE assets SET state = 'conflict' WHERE id = ?`, assetID)
	if err != nil {
		return fmt.Errorf("mark content conflict: update asset: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return fmt.Errorf("mark content conflict: rows affected: %w", err)
		}
		return fmt.Errorf("mark content conflict: %w: asset %s", errs.ErrNotFound, assetID)
	}
	return tx.Commit()
}

// FinalizeReady makes an asset visible only after every reserved operation is
// applied. Database triggers independently enforce complete file mappings.
func (r *AssetRepo) FinalizeReady(ctx context.Context, assetID string) error {
	res, err := r.rw.ExecContext(ctx, `
		UPDATE assets SET state = 'ready'
		WHERE id = ? AND state = 'pending'
		  AND NOT EXISTS (
			SELECT 1 FROM content_operations
			WHERE asset_id = assets.id AND status <> 'applied'
		  )`, assetID)
	if err != nil {
		return fmt.Errorf("finalize ready asset: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finalize ready asset: rows affected: %w", err)
	}
	if n != 1 {
		var state string
		var incomplete bool
		checkErr := r.ro.QueryRowContext(ctx, `
			SELECT state, EXISTS (
				SELECT 1 FROM content_operations
				WHERE asset_id = assets.id AND status <> 'applied'
			)
			FROM assets WHERE id = ?`, assetID,
		).Scan(&state, &incomplete)
		if checkErr == nil && AssetState(state) == AssetReady && !incomplete {
			return nil
		}
		if checkErr != nil && !errors.Is(checkErr, sql.ErrNoRows) {
			return fmt.Errorf("finalize ready asset: inspect current state: %w", checkErr)
		}
		return fmt.Errorf("finalize ready asset: %w: incomplete or terminal asset %s", errs.ErrContentConflict, assetID)
	}
	return nil
}

func validSHA256(value string) bool {
	digest, err := hex.DecodeString(value)
	return err == nil && len(digest) == sha256.Size && hex.EncodeToString(digest) == value
}

// GetAsset returns an asset by its opaque ID.
func (r *AssetRepo) GetAsset(ctx context.Context, id string) (Asset, error) {
	asset, err := scanAsset(r.ro.QueryRowContext(ctx, assetSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, fmt.Errorf("%w: asset id=%s", errs.ErrNotFound, id)
	}
	if err != nil {
		return Asset{}, fmt.Errorf("get asset: %w", err)
	}
	return asset, nil
}

// ListFiles returns an asset's files ordered by role, then opaque file ID.
func (r *AssetRepo) ListFiles(ctx context.Context, assetID string) ([]File, error) {
	var exists int
	err := r.ro.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE id = ?`, assetID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: asset id=%s", errs.ErrNotFound, assetID)
	}
	if err != nil {
		return nil, fmt.Errorf("list asset files: check asset: %w", err)
	}

	rows, err := r.ro.QueryContext(ctx,
		fileSelect+` WHERE asset_id = ? ORDER BY role, id`, assetID)
	if err != nil {
		return nil, fmt.Errorf("list asset files: %w", err)
	}
	defer rows.Close()

	var files []File
	for rows.Next() {
		file, err := scanFile(rows)
		if err != nil {
			return nil, fmt.Errorf("list asset files: scan: %w", err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list asset files: iterate: %w", err)
	}
	return files, nil
}

// GetFile returns one media file by its opaque ID.
func (r *AssetRepo) GetFile(ctx context.Context, id string) (File, error) {
	file, err := scanFile(r.ro.QueryRowContext(ctx, fileSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, fmt.Errorf("%w: file id=%s", errs.ErrNotFound, id)
	}
	if err != nil {
		return File{}, fmt.Errorf("get asset file: %w", err)
	}
	return file, nil
}

// GetPrimaryFile returns the sole primary file for an asset.
func (r *AssetRepo) GetPrimaryFile(ctx context.Context, assetID string) (File, error) {
	file, err := scanFile(r.ro.QueryRowContext(ctx,
		fileSelect+` WHERE asset_id = ? AND role = ?`, assetID, string(RolePrimary)))
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, fmt.Errorf("%w: primary file for asset id=%s", errs.ErrNotFound, assetID)
	}
	if err != nil {
		return File{}, fmt.Errorf("get primary asset file: %w", err)
	}
	return file, nil
}

// FindContentReservation returns the durable operation that owns an expected
// content identity for one owner. The reservation survives an interrupted
// Docbank create so the importer can retry the same virtual path and IDs.
func (r *AssetRepo) FindContentReservation(
	ctx context.Context,
	owner owners.Principal,
	digest string,
) (ContentReservation, error) {
	if !validSHA256(digest) {
		return ContentReservation{}, fmt.Errorf("find content reservation: %w: invalid SHA-256", errs.ErrInvalidArgument)
	}
	var (
		reservation ContentReservation
		assetState  string
		role        string
		nodeID      sql.NullInt64
		path        sql.NullString
		versionID   sql.NullString
		sha         sql.NullString
	)
	err := r.ro.QueryRowContext(ctx, `
		SELECT co.id, co.status, co.expected_sha256, co.expected_size,
		       co.docbank_virtual_path, a.state,
		       f.id, f.asset_id, f.owner_hub, f.owner_user_id, f.role,
		       f.mime_type, f.original_filename, f.import_source_path, f.size,
		       f.docbank_node_id, f.docbank_virtual_path,
		       f.current_version_id, f.sha256
		FROM content_operations AS co
		JOIN assets AS a ON a.id = co.asset_id
		JOIN media_files AS f ON f.id = co.file_id
		WHERE co.owner_hub = ? AND co.owner_user_id = ?
		  AND co.expected_sha256 = ?`, owner.Hub, owner.UserID, digest,
	).Scan(
		&reservation.OperationID, &reservation.Status, &reservation.SHA256,
		&reservation.Size, &reservation.VirtualPath, &assetState,
		&reservation.File.ID, &reservation.File.AssetID,
		&reservation.File.Owner.Hub, &reservation.File.Owner.UserID, &role,
		&reservation.File.MimeType, &reservation.File.OriginalFilename,
		&reservation.File.ImportSourcePath, &reservation.File.Size, &nodeID,
		&path, &versionID, &sha,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ContentReservation{}, fmt.Errorf("find content reservation: %w: SHA-256 %s", errs.ErrNotFound, digest)
	}
	if err != nil {
		return ContentReservation{}, fmt.Errorf("find content reservation: %w", err)
	}
	reservation.AssetState = AssetState(assetState)
	reservation.File.Role = FileRole(role)
	reservation.File.DocbankNodeID = int64FromNull(nodeID)
	reservation.File.DocbankVirtualPath = path.String
	reservation.File.CurrentVersionID = versionID.String
	reservation.File.SHA256 = sha.String
	return reservation, nil
}

func scanAsset(scanner rowScanner) (Asset, error) {
	var (
		asset          Asset
		state          string
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
	if err := scanner.Scan(
		&asset.ID,
		&asset.Owner.Hub,
		&asset.Owner.UserID,
		&state,
		&mediaType,
		&asset.ImportedAt,
		&timestamp,
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
		&asset.ThumbStatus,
		&asset.ThumbVersion,
		&thumbUpdatedAt,
		&hiddenAt,
	); err != nil {
		return Asset{}, err
	}

	asset.State = AssetState(state)
	asset.Type = Type(mediaType)
	asset.Timestamp = timeFromNull(timestamp)
	asset.Make = makeN.String
	asset.Model = modelN.String
	asset.LensModel = lensModel.String
	asset.FocalLength = focalLength.String
	asset.Shutter = shutter.String
	asset.Width = intFromNull(width)
	asset.Height = intFromNull(height)
	asset.ISO = intFromNull(iso)
	asset.Aperture = floatFromNull(aperture)
	asset.DurationMs = int64FromNull(durationMs)
	asset.Latitude = floatFromNull(latitude)
	asset.Longitude = floatFromNull(longitude)
	asset.GPSAt = timeFromNull(gpsAt)
	asset.LocationLabel = locationLabel.String
	asset.ThumbUpdatedAt = timeFromNull(thumbUpdatedAt)
	asset.HiddenAt = timeFromNull(hiddenAt)
	return asset, nil
}

func scanFile(scanner rowScanner) (File, error) {
	var (
		file        File
		role        string
		nodeID      sql.NullInt64
		virtualPath sql.NullString
		versionID   sql.NullString
		sha         sql.NullString
	)
	if err := scanner.Scan(
		&file.ID,
		&file.AssetID,
		&file.Owner.Hub,
		&file.Owner.UserID,
		&role,
		&file.MimeType,
		&file.OriginalFilename,
		&file.ImportSourcePath,
		&file.Size,
		&nodeID,
		&virtualPath,
		&versionID,
		&sha,
	); err != nil {
		return File{}, err
	}
	file.Role = FileRole(role)
	file.DocbankNodeID = int64FromNull(nodeID)
	file.DocbankVirtualPath = virtualPath.String
	file.CurrentVersionID = versionID.String
	file.SHA256 = sha.String
	return file, nil
}

func intFromNull(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}

func int64FromNull(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func floatFromNull(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func timeFromNull(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func validateAssetGraphInput(
	asset Asset,
	files []File,
	relationships []FileRelationship,
) error {
	if err := validateOpaqueUUID(asset.ID, "asset ID"); err != nil {
		return err
	}
	if asset.Owner.Hub == "" || asset.Owner.UserID == "" {
		return fmt.Errorf("%w: incomplete asset owner", errs.ErrInvalidArgument)
	}
	if err := ValidateAssetState(asset.State); err != nil {
		return err
	}
	switch asset.Type {
	case TypePhoto, TypeVideo:
	default:
		return fmt.Errorf("%w: invalid media type %q", errs.ErrInvalidArgument, asset.Type)
	}
	if err := validateGPSPair(asset.Latitude, asset.Longitude); err != nil {
		return err
	}

	fileIDs := make(map[string]struct{}, len(files))
	for _, file := range files {
		if err := validateOpaqueUUID(file.ID, "file ID"); err != nil {
			return err
		}
		if file.AssetID != asset.ID || file.Owner != asset.Owner {
			return fmt.Errorf("%w: file %s does not belong to asset", errs.ErrInvalidArgument, file.ID)
		}
		if err := ValidateFileRole(file.Role); err != nil {
			return err
		}
		fileIDs[file.ID] = struct{}{}
	}
	for _, relationship := range relationships {
		if err := validateOpaqueUUID(relationship.SourceFileID, "relationship source file ID"); err != nil {
			return err
		}
		if err := validateOpaqueUUID(relationship.TargetFileID, "relationship target file ID"); err != nil {
			return err
		}
		if err := ValidateRelationshipKind(relationship.Kind); err != nil {
			return err
		}
		if _, ok := fileIDs[relationship.SourceFileID]; !ok {
			return fmt.Errorf("%w: relationship source file is outside asset graph", errs.ErrInvalidArgument)
		}
		if _, ok := fileIDs[relationship.TargetFileID]; !ok {
			return fmt.Errorf("%w: relationship target file is outside asset graph", errs.ErrInvalidArgument)
		}
	}
	return nil
}

func validateOpaqueUUID(value, field string) error {
	id, err := uuid.Parse(value)
	if err != nil || id.Version() != 4 || id.String() != value {
		return fmt.Errorf("%w: invalid %s", errs.ErrInvalidArgument, field)
	}
	return nil
}

func validateDocbankMapping(file File, ownerStorageKey string) error {
	absent := file.DocbankNodeID == nil &&
		file.DocbankVirtualPath == "" &&
		file.CurrentVersionID == "" && file.SHA256 == ""
	if absent {
		return nil
	}
	if file.DocbankNodeID == nil || *file.DocbankNodeID <= 0 {
		return fmt.Errorf("%w: invalid Docbank node ID", errs.ErrInvalidArgument)
	}
	versionID, err := uuid.Parse(file.CurrentVersionID)
	if err != nil || versionID.Version() != 4 || versionID.String() != file.CurrentVersionID {
		return fmt.Errorf("%w: invalid Docbank version ID", errs.ErrInvalidArgument)
	}
	digest, err := hex.DecodeString(file.SHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != file.SHA256 {
		return fmt.Errorf("%w: invalid Docbank SHA-256", errs.ErrInvalidArgument)
	}
	expectedPath, err := content.VirtualPath(ownerStorageKey, file.ID, file.OriginalFilename)
	if err != nil || file.DocbankVirtualPath != expectedPath {
		return fmt.Errorf("%w: invalid Docbank virtual path", errs.ErrInvalidArgument)
	}
	return nil
}

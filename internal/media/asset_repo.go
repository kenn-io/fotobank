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

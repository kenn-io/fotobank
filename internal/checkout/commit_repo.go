package checkout

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mattn/go-sqlite3"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/search/index"
)

func (r *Repo) ListPendingEntries(ctx context.Context, checkoutID string) ([]Entry, error) {
	rows, err := r.ro.QueryContext(ctx, `SELECT checkout_id, file_id,
		relative_path, base_version_id, base_sha256, base_size, observed_size,
		observed_mtime, observed_identity, observed_sha256, state, last_error,
		created_at, updated_at
		FROM checkout_entries WHERE checkout_id = ? AND state = ? ORDER BY relative_path`,
		checkoutID, string(EntryPending))
	if err != nil {
		return nil, fmt.Errorf("list pending checkout entries: %w", err)
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		entry, err := scanCommitEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("list pending checkout entries: scan: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pending checkout entries: iterate: %w", err)
	}
	return entries, nil
}

func (r *Repo) GetCommitTarget(ctx context.Context, checkoutID, fileID string) (CommitTarget, error) {
	row := r.ro.QueryRowContext(ctx, `SELECT e.checkout_id, e.file_id,
		e.relative_path, e.base_version_id, e.base_sha256, e.base_size,
		e.observed_size, e.observed_mtime, e.observed_identity, e.observed_sha256,
		e.state, e.last_error, e.created_at, e.updated_at,
		f.asset_id, f.role, f.mime_type, f.docbank_node_id, f.docbank_virtual_path
		FROM checkout_entries e
		JOIN checkouts c ON c.id = e.checkout_id AND c.state = 'active'
		JOIN media_files f ON f.id = e.file_id
		  AND f.owner_hub = c.owner_hub AND f.owner_user_id = c.owner_user_id
		JOIN assets a ON a.id = f.asset_id AND a.state = 'ready' AND a.hidden_at IS NULL
		WHERE e.checkout_id = ? AND e.file_id = ?
		  AND f.current_version_id = e.base_version_id
		  AND f.sha256 = e.base_sha256 AND f.size = e.base_size
		  AND NOT EXISTS (
			SELECT 1 FROM media_files duplicate
			WHERE duplicate.owner_hub = c.owner_hub
			  AND duplicate.owner_user_id = c.owner_user_id
			  AND duplicate.sha256 = e.observed_sha256
			  AND duplicate.id <> f.id
		  )`, checkoutID, fileID)
	var target CommitTarget
	var state string
	var lastError sql.NullString
	err := row.Scan(
		&target.Entry.CheckoutID, &target.Entry.FileID, &target.Entry.RelativePath,
		&target.Entry.BaseVersionID, &target.Entry.BaseSHA256, &target.Entry.BaseSize,
		&target.Entry.ObservedSize, &target.Entry.ObservedMTime,
		&target.Entry.ObservedIdentity, &target.Entry.ObservedSHA256,
		&state, &lastError, &target.Entry.CreatedAt, &target.Entry.UpdatedAt,
		&target.AssetID, &target.Role, &target.MediaType, &target.NodeID, &target.VirtualPath,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CommitTarget{}, fmt.Errorf("get checkout commit target: %w", errs.ErrNotFound)
	}
	if err != nil {
		return CommitTarget{}, fmt.Errorf("get checkout commit target: %w", err)
	}
	target.Entry.State = EntryState(state)
	target.Entry.LastError = lastError.String
	return target, nil
}

func (r *Repo) ApplyCommit(
	ctx context.Context,
	target CommitTarget,
	receipt CommitReceipt,
	clean bool,
	now time.Time,
) error {
	if receipt.NodeID != target.NodeID || receipt.NodeID <= 0 ||
		uuid.Validate(receipt.VersionID) != nil || !validCommitSHA(receipt.SHA256) || receipt.Size < 0 {
		return fmt.Errorf("apply checkout commit: %w: invalid Docbank receipt", errs.ErrInvalidArgument)
	}
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply checkout commit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var assetID, role string
	var nodeID int64
	var virtualPath, currentVersion, currentSHA string
	var currentSize int64
	err = tx.QueryRowContext(ctx, `SELECT f.asset_id, f.role, f.docbank_node_id,
		f.docbank_virtual_path, f.current_version_id, f.sha256, f.size
		FROM media_files f
		JOIN checkout_entries e ON e.checkout_id = ? AND e.file_id = f.id
		JOIN checkouts c ON c.id = e.checkout_id
		  AND c.owner_hub = f.owner_hub AND c.owner_user_id = f.owner_user_id
		JOIN assets a ON a.id = f.asset_id AND a.state = 'ready' AND a.hidden_at IS NULL
		WHERE f.id = ?`, target.Entry.CheckoutID, target.Entry.FileID).Scan(
		&assetID, &role, &nodeID, &virtualPath, &currentVersion, &currentSHA, &currentSize)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("apply checkout commit: %w: source file is unavailable", errs.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("apply checkout commit: read source file: %w", err)
	}
	if assetID != target.AssetID || role != target.Role || nodeID != target.NodeID ||
		virtualPath != target.VirtualPath || currentVersion != target.Entry.BaseVersionID ||
		currentSHA != target.Entry.BaseSHA256 || currentSize != target.Entry.BaseSize {
		return fmt.Errorf("apply checkout commit: %w: source authority changed", errs.ErrContentConflict)
	}

	res, err := tx.ExecContext(ctx, `UPDATE media_files
		SET current_version_id = ?, sha256 = ?, size = ?
		WHERE id = ? AND docbank_node_id = ? AND docbank_virtual_path = ?
		  AND current_version_id = ? AND sha256 = ? AND size = ?`,
		receipt.VersionID, receipt.SHA256, receipt.Size,
		target.Entry.FileID, target.NodeID, target.VirtualPath,
		target.Entry.BaseVersionID, target.Entry.BaseSHA256, target.Entry.BaseSize)
	if err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
			return fmt.Errorf("apply checkout commit: %w: content identity is already recorded",
				errs.ErrContentConflict)
		}
		return fmt.Errorf("apply checkout commit: update source file: %w", err)
	}
	if changed, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("apply checkout commit: source rows affected: %w", err)
	} else if changed != 1 {
		return fmt.Errorf("apply checkout commit: %w: source authority changed", errs.ErrContentConflict)
	}

	state := EntryPending
	message := "working file changed while its prior bytes were committed; rescan required"
	if clean {
		state = EntryClean
		message = ""
	}
	res, err = tx.ExecContext(ctx, `UPDATE checkout_entries SET
		base_version_id = ?, base_sha256 = ?, base_size = ?, state = ?,
		last_error = NULLIF(?, ''), updated_at = ?
		WHERE checkout_id = ? AND file_id = ? AND state = ?
		  AND base_version_id = ? AND base_sha256 = ? AND base_size = ?
		  AND observed_size = ? AND observed_mtime = ?
		  AND observed_identity = ? AND observed_sha256 = ?`,
		receipt.VersionID, receipt.SHA256, receipt.Size, string(state), message, now.UTC(),
		target.Entry.CheckoutID, target.Entry.FileID, string(EntryPending),
		target.Entry.BaseVersionID, target.Entry.BaseSHA256, target.Entry.BaseSize,
		target.Entry.ObservedSize, target.Entry.ObservedMTime.UTC(),
		target.Entry.ObservedIdentity, target.Entry.ObservedSHA256)
	if err != nil {
		return fmt.Errorf("apply checkout commit: update checkout entry: %w", err)
	}
	if changed, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("apply checkout commit: entry rows affected: %w", err)
	} else if changed != 1 {
		return fmt.Errorf("apply checkout commit: %w: checkout observation changed", errs.ErrContentConflict)
	}
	if !clean {
		if _, err := tx.ExecContext(ctx, `UPDATE checkout_entries
			SET observed_identity = ''
			WHERE checkout_id = ? AND file_id = ?`,
			target.Entry.CheckoutID, target.Entry.FileID); err != nil {
			return fmt.Errorf("apply checkout commit: invalidate checkout observation: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM checkout_scan_candidates
			WHERE checkout_id = ? AND relative_path = ?`,
			target.Entry.CheckoutID, target.Entry.RelativePath); err != nil {
			return fmt.Errorf("apply checkout commit: reset checkout observation: %w", err)
		}
	}

	if role == "primary" {
		if err := invalidatePrimaryProjections(ctx, tx, assetID, now.UTC()); err != nil {
			return fmt.Errorf("apply checkout commit: invalidate projections: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apply checkout commit: commit: %w", err)
	}
	return nil
}

func (r *Repo) MarkCommitConflict(
	ctx context.Context,
	entry Entry,
	cause error,
	now time.Time,
) (bool, error) {
	message := "content conflict"
	if cause != nil {
		message = cause.Error()
	}
	res, err := r.rw.ExecContext(ctx, `UPDATE checkout_entries SET
		state = ?, last_error = ?, updated_at = ?
		WHERE checkout_id = ? AND file_id = ? AND state = ?
		  AND base_version_id = ? AND observed_sha256 = ?`,
		string(EntryConflict), message, now.UTC(), entry.CheckoutID, entry.FileID,
		string(EntryPending), entry.BaseVersionID, entry.ObservedSHA256)
	if err != nil {
		return false, fmt.Errorf("mark checkout commit conflict: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark checkout commit conflict: rows affected: %w", err)
	}
	if changed == 1 {
		return true, nil
	}
	var state EntryState
	if err := r.ro.QueryRowContext(ctx, `SELECT state FROM checkout_entries
		WHERE checkout_id = ? AND file_id = ?`, entry.CheckoutID, entry.FileID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("mark checkout commit conflict: %w", errs.ErrNotFound)
		}
		return false, fmt.Errorf("mark checkout commit conflict: inspect entry: %w", err)
	}
	return state == EntryConflict, nil
}

func invalidatePrimaryProjections(ctx context.Context, tx *sql.Tx, assetID string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE assets SET
		timestamp = NULL, make = NULL, model = NULL, lens_model = NULL,
		focal_length = NULL, shutter = NULL, width = NULL, height = NULL,
		iso = NULL, aperture = NULL, duration_ms = NULL, latitude = NULL,
		longitude = NULL, gps_at = NULL, location_label = NULL,
		source_metadata_version_id = NULL,
		source_metadata_extractor_fingerprint = NULL,
		source_metadata_checksum = NULL,
		thumb_status = 'pending', thumb_version = thumb_version + 1,
		thumb_updated_at = ?, thumb_claimed_at = NULL
		WHERE id = ?`, now, assetID); err != nil {
		return fmt.Errorf("queue thumbnail: %w", err)
	}
	if err := embedding.OnThumbRegen(ctx, tx, assetID); err != nil {
		return fmt.Errorf("invalidate embeddings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_results SET status = 'stale'
		WHERE media_id = ? AND status = 'active'`, assetID); err != nil {
		return fmt.Errorf("stale AI results: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_jobs SET
		status = 'superseded', completed_at = ?, last_error_kind = ?,
		last_error = 'content_changed'
		WHERE media_id = ? AND status IN ('pending', 'working', 'blocked')`,
		now, string(ai.ErrKindSuperseded), assetID); err != nil {
		return fmt.Errorf("supersede AI jobs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_failures WHERE media_id = ?`, assetID); err != nil {
		return fmt.Errorf("clear AI failures: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_skipped WHERE media_id = ?`, assetID); err != nil {
		return fmt.Errorf("clear AI skips: %w", err)
	}
	if err := index.RefreshMediaFTS(ctx, tx, assetID); err != nil {
		return fmt.Errorf("refresh search corpus: %w", err)
	}
	return nil
}

func scanCommitEntry(row interface{ Scan(...any) error }) (Entry, error) {
	var entry Entry
	var state string
	var lastError sql.NullString
	err := row.Scan(
		&entry.CheckoutID, &entry.FileID, &entry.RelativePath,
		&entry.BaseVersionID, &entry.BaseSHA256, &entry.BaseSize,
		&entry.ObservedSize, &entry.ObservedMTime, &entry.ObservedIdentity,
		&entry.ObservedSHA256, &state, &lastError, &entry.CreatedAt, &entry.UpdatedAt,
	)
	entry.State = EntryState(state)
	entry.LastError = lastError.String
	return entry, err
}

func validCommitSHA(value string) bool {
	digest, err := hex.DecodeString(value)
	return err == nil && len(digest) == sha256.Size && hex.EncodeToString(digest) == value
}

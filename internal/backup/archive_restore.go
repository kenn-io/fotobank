package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"go.kenn.io/fotobank/internal/content"
)

type ArchiveRestoreReport struct {
	SnapshotID         string `json:"snapshot_id"`
	VaultRoot          string `json:"vault_root"`
	CatalogPath        string `json:"catalog_path"`
	ReferencesVerified int64  `json:"references_verified"`
}

// RestoreArchive restores into a separate, empty target, then checks the
// captured catalog and its immutable references. Failure leaves the isolated
// target available for diagnosis; it is never reported as a usable recovery.
func RestoreArchive(ctx context.Context, repository *content.BackupRepository,
	snapshotID, target string, protectedRoots []string,
) (result ArchiveRestoreReport, retErr error) {
	report, err := repository.Restore(ctx, content.BackupRestoreOptions{
		SnapshotID: snapshotID, Target: target, ProtectedRoots: protectedRoots,
	})
	if err != nil {
		return result, err
	}
	catalog := filepath.Join(report.Target, filepath.FromSlash(ArchiveCatalogPath))
	if err := ValidateSnapshot(ctx, catalog); err != nil {
		return result, fmt.Errorf("validate restored catalog at %s: %w", catalog, err)
	}
	database, err := openSnapshotSource(catalog)
	if err != nil {
		return result, err
	}
	defer func() { retErr = errors.Join(retErr, database.Close()) }()
	vault, err := content.Open(ctx, content.Config{Root: report.Target})
	if err != nil {
		return result, err
	}
	defer func() { retErr = errors.Join(retErr, vault.Close()) }()
	// Current mappings, import receipts, and retained checkout bases can name
	// different immutable versions. Do not require the catalog's version to be
	// the Docbank head: a later append can legitimately be in the same backup.
	rows, err := database.QueryContext(ctx, `
		SELECT docbank_node_id, current_version_id, sha256, size, docbank_virtual_path
		FROM media_files WHERE current_version_id IS NOT NULL
		UNION
		SELECT docbank_node_id, docbank_version_id, expected_sha256, expected_size, docbank_virtual_path
		FROM content_operations WHERE docbank_node_id IS NOT NULL AND docbank_version_id IS NOT NULL
		UNION
		SELECT COALESCE(f.docbank_node_id, 0), e.base_version_id, e.base_sha256, e.base_size,
		       f.docbank_virtual_path
		FROM checkout_entries e LEFT JOIN media_files f ON f.id=e.file_id`)
	if err != nil {
		return result, fmt.Errorf("read restored catalog references: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, rows.Close()) }()
	var count int64
	for rows.Next() {
		var nodeID, size int64
		var versionID, digest string
		var virtualPath sql.NullString
		if err := rows.Scan(&nodeID, &versionID, &digest, &size, &virtualPath); err != nil {
			return result, err
		}
		// Detached checkout bases have no retained virtual path. For mapped
		// references, check only path-to-node identity, not the current head.
		if virtualPath.Valid {
			node, err := vault.Stat(ctx, virtualPath.String)
			if err != nil {
				return result, fmt.Errorf("restore path for version %s: %w", versionID, err)
			}
			if node.ID != nodeID {
				return result, fmt.Errorf("restored path for version %s does not name its catalog node", versionID)
			}
		}
		opened, err := vault.OpenVersion(ctx, versionID)
		if err != nil {
			return result, fmt.Errorf("restore reference %s: %w", versionID, err)
		}
		if (nodeID != 0 && opened.NodeID != nodeID) || opened.SHA256 != digest || opened.Size != size {
			return result, errors.Join(fmt.Errorf("restored version %s does not match its catalog reference", versionID), opened.Reader.Close())
		}
		_, readErr := io.Copy(io.Discard, opened.Reader)
		verifyErr := opened.Reader.Verify()
		closeErr := opened.Reader.Close()
		if err := errors.Join(readErr, verifyErr, closeErr); err != nil {
			return result, fmt.Errorf("verify restored version %s: %w", versionID, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return ArchiveRestoreReport{SnapshotID: report.SnapshotID, VaultRoot: report.Target,
		CatalogPath: catalog, ReferencesVerified: count}, nil
}

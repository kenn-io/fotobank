package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.kenn.io/fotobank/internal/content"
)

// ArchiveCatalogPath is the catalog's name inside a complete recovery point.
const ArchiveCatalogPath = "application/catalog.sqlite"

// CreateArchive captures the catalog during Docbank's mutation freeze. The
// caller owns the vault and must retain the database lifetime lock until this
// function returns. The catalog contains non-secret settings and credential
// hashes, not configuration files or provider credentials.
func CreateArchive(ctx context.Context, databasePath string, vault *content.Adapter,
	repository *content.BackupRepository, tag string,
) (content.BackupSnapshot, error) {
	info, err := os.Stat(databasePath)
	if err != nil {
		return content.BackupSnapshot{}, fmt.Errorf("inspect archive catalog: %w", err)
	}
	if !info.Mode().IsRegular() {
		return content.BackupSnapshot{}, fmt.Errorf("archive catalog must be a regular SQLite file")
	}
	staging, err := os.MkdirTemp("", "fotobank-archive-")
	if err != nil {
		return content.BackupSnapshot{}, fmt.Errorf("create archive staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	catalog := filepath.Join(staging, "catalog.sqlite")
	return vault.CreateBackup(ctx, repository, content.BackupOptions{
		Tag: tag,
		Prepare: func(ctx context.Context) error {
			return SnapshotPath(ctx, databasePath, catalog)
		},
		ExtraFiles: []content.BackupExtraFile{{Path: catalog, RecordAs: ArchiveCatalogPath}},
	})
}

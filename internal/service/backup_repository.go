package service

import (
	"context"
	"fmt"
	"path/filepath"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
)

// BackupRepositoryService is host-operator-only and needs no source storage.
// The operator listener supplies authentication, not a photo-owner identity.
type BackupRepositoryService struct{}

type BackupRepositoryInfo struct {
	ID   string `json:"id"`
	Root string `json:"root"`
}

func validateRepositoryPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: backup repository must be an absolute path on the server host", errs.ErrInvalidArgument)
	}
	return nil
}

func (*BackupRepositoryService) Init(path string) (BackupRepositoryInfo, error) {
	if err := validateRepositoryPath(path); err != nil {
		return BackupRepositoryInfo{}, err
	}
	repo, err := content.InitBackupRepository(path)
	if err != nil {
		return BackupRepositoryInfo{}, err
	}
	return BackupRepositoryInfo{ID: repo.ID(), Root: repo.Root()}, nil
}

func (*BackupRepositoryService) List(path string) ([]content.BackupSnapshot, error) {
	if err := validateRepositoryPath(path); err != nil {
		return nil, err
	}
	repo, err := content.OpenBackupRepository(path)
	if err != nil {
		return nil, err
	}
	return repo.Snapshots()
}

func (*BackupRepositoryService) Verify(ctx context.Context, path, snapshotID string, all bool) (content.BackupVerifyReport, error) {
	if err := validateRepositoryPath(path); err != nil {
		return content.BackupVerifyReport{}, err
	}
	if all && snapshotID != "" {
		return content.BackupVerifyReport{}, fmt.Errorf("%w: all and snapshot_id are mutually exclusive", errs.ErrInvalidArgument)
	}
	repo, err := content.OpenBackupRepository(path)
	if err != nil {
		return content.BackupVerifyReport{}, err
	}
	return repo.Verify(ctx, content.BackupVerifyOptions{SnapshotID: snapshotID, All: all})
}

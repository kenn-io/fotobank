package service

import (
	"context"
	"fmt"
	"path/filepath"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
)

// ArchiveRestoreService is installed only on the recovery-mode operator
// listener. Source configuration and database selection belong to the daemon,
// never to the HTTP caller. No source catalog or vault is opened.
type ArchiveRestoreService struct {
	configPath   string
	databasePath string
	storageRoots [4]string
}

func NewArchiveRestoreService(configPath, databasePath string, cfg *config.Config) *ArchiveRestoreService {
	return &ArchiveRestoreService{configPath: configPath, databasePath: databasePath, storageRoots: archiveStorageRoots(cfg)}
}

func archiveStorageRoots(cfg *config.Config) [4]string {
	return [4]string{cfg.Docbank.Root, cfg.ConfiguredNASRoot(), cfg.ConfiguredFlashRoot(), cfg.Backup.Repository}
}

func (s *ArchiveRestoreService) Restore(ctx context.Context, repositoryPath, snapshotID, target string) (backup.ArchiveRestoreReport, error) {
	if err := validateRepositoryPath(repositoryPath); err != nil {
		return backup.ArchiveRestoreReport{}, err
	}
	if !filepath.IsAbs(target) {
		return backup.ArchiveRestoreReport{}, fmt.Errorf("%w: restore target must be an absolute path on the server host", errs.ErrInvalidArgument)
	}
	cfg, err := config.LoadUnchecked(s.configPath)
	if err != nil {
		return backup.ArchiveRestoreReport{}, err
	}
	if archiveStorageRoots(cfg) != s.storageRoots {
		return backup.ArchiveRestoreReport{}, fmt.Errorf("%w: check the source paths and run fotobank daemon restart --recovery", errs.ErrBackupRestoreConfigurationChanged)
	}
	configuredVault := cfg.Docbank.Root
	if err := cfg.ValidateWithOptions(config.ValidationOptions{AllowUnavailableStorage: true}); err != nil {
		return backup.ArchiveRestoreReport{}, err
	}
	protected := []string{configuredVault, cfg.Docbank.Root, cfg.ConfiguredNASRoot(), cfg.NAS.Root,
		cfg.ConfiguredFlashRoot(), cfg.Flash.Root, cfg.Backup.Repository, repositoryPath}
	target, protected, err = config.ArchiveRestorePaths(target, s.databasePath, protected)
	if err != nil {
		return backup.ArchiveRestoreReport{}, err
	}
	repository, err := content.OpenBackupRepository(repositoryPath)
	if err != nil {
		return backup.ArchiveRestoreReport{}, err
	}
	return backup.RestoreArchive(ctx, repository, snapshotID, target, protected)
}

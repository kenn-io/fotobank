package service

import (
	"context"
	"fmt"
	"path/filepath"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

// BackupService grants the configured local operator whole-deployment capture,
// including all owners and hidden media. It is not a photo-user service.
type BackupService struct {
	owner        owners.Principal
	databasePath string
	vault        *content.Adapter
}

func NewBackupService(owner owners.Principal, databasePath string, vault *content.Adapter) *BackupService {
	return &BackupService{owner: owner, databasePath: databasePath, vault: vault}
}

func (s *BackupService) Create(ctx context.Context, caller owners.Principal, repositoryPath, tag string) (content.BackupSnapshot, error) {
	if caller != s.owner {
		return content.BackupSnapshot{}, fmt.Errorf("backup operator does not match: %w", errs.ErrPermissionDenied)
	}
	if tag == backup.ScheduledTag {
		return content.BackupSnapshot{}, fmt.Errorf("%w: this tag is reserved for scheduled recovery points", errs.ErrInvalidArgument)
	}
	if !filepath.IsAbs(repositoryPath) {
		return content.BackupSnapshot{}, fmt.Errorf("%w: backup repository must be an absolute path on the server host", errs.ErrInvalidArgument)
	}
	repository, err := content.OpenBackupRepository(repositoryPath)
	if err != nil {
		return content.BackupSnapshot{}, err
	}
	return backup.CreateArchive(ctx, s.databasePath, s.vault, repository, tag)
}

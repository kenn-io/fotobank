package client

import (
	"context"
	"errors"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/service"
)

func RestoreArchive(ctx context.Context, configPath, version string, input httpapi.ArchiveRestoreRequest) (backup.ArchiveRestoreReport, error) {
	var out backup.ArchiveRestoreReport
	err := call(ctx, configPath, version, &out, "inspect the restore target before retrying; restore never overwrites an existing recovery", func(c *generated.Client) (*generated.RestoreBackupArchiveResponse, error) {
		return c.RestoreBackupArchive(ctx, &generated.RestoreBackupArchiveRequestOptions{Body: &input})
	})
	return out, err
}

func InitBackupRepository(ctx context.Context, configPath, version, path string) (service.BackupRepositoryInfo, error) {
	var out service.BackupRepositoryInfo
	err := call(ctx, configPath, version, &out, "inspect the repository before retrying", func(c *generated.Client) (*generated.InitBackupRepositoryResponse, error) {
		return c.InitBackupRepository(ctx, &generated.InitBackupRepositoryRequestOptions{Body: &httpapi.BackupRepositoryRequest{Repository: path}})
	})
	return out, err
}

func ListBackupSnapshots(ctx context.Context, configPath, version, path string) ([]content.BackupSnapshot, error) {
	var out generated.ListBackupSnapshotsResponse
	err := call(ctx, configPath, version, &out, "retry backup list", func(c *generated.Client) (*generated.ListBackupSnapshotsResponse, error) {
		return c.ListBackupSnapshots(ctx, &generated.ListBackupSnapshotsRequestOptions{Query: &generated.ListBackupSnapshotsQuery{Repository: path}})
	})
	return out, err
}

func VerifyBackupRepository(ctx context.Context, configPath, version string, input httpapi.BackupRepositoryVerifyRequest) (content.BackupVerifyReport, error) {
	var out content.BackupVerifyReport
	err := call(ctx, configPath, version, &out, "retry backup verify", func(c *generated.Client) (*generated.VerifyBackupRepositoryResponse, error) {
		return c.VerifyBackupRepository(ctx, &generated.VerifyBackupRepositoryRequestOptions{Body: &input})
	})
	return out, err
}

func CreateBackup(ctx context.Context, configPath, version string, request httpapi.BackupRequest) (content.BackupSnapshot, error) {
	var out httpapi.BackupResult
	err := call(ctx, configPath, version, &out, "list and verify the backup repository before retrying", func(c *generated.Client) (*generated.CreateBackupResponse, error) {
		return c.CreateBackup(ctx, &generated.CreateBackupRequestOptions{Body: &request})
	})
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out.Snapshot, err
}

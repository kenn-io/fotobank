package client

import (
	"context"
	"errors"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/service"
	"net/http"
	"net/url"
)

func InitBackupRepository(ctx context.Context, configPath, version, path string) (service.BackupRepositoryInfo, error) {
	var out service.BackupRepositoryInfo
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/operator/backup-repository/init", httpapi.BackupRepositoryRequest{Repository: path}, &out, "inspect the repository before retrying")
	return out, err
}

func ListBackupSnapshots(ctx context.Context, configPath, version, path string) ([]content.BackupSnapshot, error) {
	var out []content.BackupSnapshot
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/operator/backup-repository/snapshots?repository="+url.QueryEscape(path), nil, &out, "retry backup list")
	return out, err
}

func VerifyBackupRepository(ctx context.Context, configPath, version string, input httpapi.BackupRepositoryVerifyRequest) (content.BackupVerifyReport, error) {
	var out content.BackupVerifyReport
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/operator/backup-repository/verify", input, &out, "retry backup verify")
	return out, err
}

func CreateBackup(ctx context.Context, configPath, version string, request httpapi.BackupRequest) (content.BackupSnapshot, error) {
	var out httpapi.BackupResult
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/operator/backups", request, &out, "list and verify the backup repository before retrying")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out.Snapshot, err
}

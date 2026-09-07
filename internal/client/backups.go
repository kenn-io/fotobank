package client

import (
	"context"
	"errors"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/httpapi"
	"net/http"
)

func CreateBackup(ctx context.Context, dbPath, version string, request httpapi.BackupRequest) (content.BackupSnapshot, error) {
	var out httpapi.BackupResult
	err := call(ctx, dbPath, version, http.MethodPost, "/api/v1/operator/backups", request, &out, "list and verify the backup repository before retrying")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out.Snapshot, err
}

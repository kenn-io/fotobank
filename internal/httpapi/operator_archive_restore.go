package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/service"
)

type ArchiveRestoreRequest struct {
	Repository string `json:"repository" minLength:"1" maxLength:"4096"`
	SnapshotID string `json:"snapshot_id,omitempty" maxLength:"256"`
	Target     string `json:"target" minLength:"1" maxLength:"4096"`
}

func registerArchiveRestore(api huma.API, svc *service.ArchiveRestoreService) {
	huma.Register(api, huma.Operation{
		OperationID: "restore-backup-archive", Method: http.MethodPost, Path: "/api/v1/operator/backup-repository/restore",
		Summary: "Restore and verify an archive into a separate target", Tags: []string{"operator"},
		Description: "Requires the local operator credential and a recovery-mode daemon. The target must be empty and separate from source storage and the backup repository. Does not activate the restored deployment.",
		Security:    []map[string][]string{{"localOperator": {}}}, MaxBodyBytes: 16384,
	}, func(ctx context.Context, input *struct{ Body ArchiveRestoreRequest }) (*struct{ Body backup.ArchiveRestoreReport }, error) {
		if svc == nil {
			return nil, huma.Error403Forbidden("archive restore requires a recovery-mode daemon and local operator authentication")
		}
		report, err := svc.Restore(ctx, input.Body.Repository, input.Body.SnapshotID, input.Body.Target)
		if err != nil {
			return nil, backupRepositoryError(err)
		}
		return &struct{ Body backup.ArchiveRestoreReport }{report}, nil
	})
}

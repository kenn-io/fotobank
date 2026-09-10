package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/service"
)

type BackupRepositoryRequest struct {
	Repository string `json:"repository" minLength:"1" maxLength:"4096"`
}

type BackupRepositoryVerifyRequest struct {
	Repository string `json:"repository" minLength:"1" maxLength:"4096"`
	SnapshotID string `json:"snapshot_id,omitempty" maxLength:"256"`
	All        bool   `json:"all"`
}

func registerBackupRepository(api huma.API, svc *service.BackupRepositoryService) {
	huma.Register(api, huma.Operation{
		OperationID: "init-backup-repository", Method: http.MethodPost, Path: "/api/v1/operator/backup-repository/init",
		Summary: "Initialize a backup repository", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}}, MaxBodyBytes: 16384,
	}, func(_ context.Context, input *struct{ Body BackupRepositoryRequest }) (*struct{ Body service.BackupRepositoryInfo }, error) {
		if svc == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		out, err := svc.Init(input.Body.Repository)
		if err != nil {
			return nil, backupRepositoryError(err)
		}
		return &struct{ Body service.BackupRepositoryInfo }{out}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-backup-snapshots", Method: http.MethodGet, Path: "/api/v1/operator/backup-repository/snapshots",
		Summary: "List backup recovery points", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}},
	}, func(_ context.Context, input *struct {
		Repository string `query:"repository" required:"true" minLength:"1" maxLength:"4096"`
	}) (*struct{ Body []content.BackupSnapshot }, error) {
		if svc == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		out, err := svc.List(input.Repository)
		if err != nil {
			return nil, backupRepositoryError(err)
		}
		return &struct{ Body []content.BackupSnapshot }{out}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "verify-backup-repository", Method: http.MethodPost, Path: "/api/v1/operator/backup-repository/verify",
		Summary: "Verify backup recovery points", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}}, MaxBodyBytes: 16384,
	}, func(ctx context.Context, input *struct{ Body BackupRepositoryVerifyRequest }) (*struct{ Body content.BackupVerifyReport }, error) {
		if svc == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		out, err := svc.Verify(ctx, input.Body.Repository, input.Body.SnapshotID, input.Body.All)
		if err != nil {
			return nil, backupRepositoryError(err)
		}
		return &struct{ Body content.BackupVerifyReport }{out}, nil
	})
}

// This listener is host-operator-only. Keep actionable repository diagnostics
// that the CLI previously returned, rather than the photo API's generic errors.
func backupRepositoryError(err error) huma.StatusError {
	if errors.Is(err, errs.ErrBackupRepositoryLocked) {
		return huma.Error409Conflict(err.Error())
	}
	return huma.NewError(Translate(err).GetStatus(), err.Error())
}

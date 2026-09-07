package operator

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
)

type BackupRequest struct {
	Hub        string `json:"hub"`
	UserID     string `json:"user_id"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

type BackupResult struct {
	Snapshot content.BackupSnapshot `json:"snapshot"`
	Error    string                 `json:"error,omitempty"`
}

func registerBackups(api huma.API, backups *service.BackupService) {
	huma.Register(api, huma.Operation{
		OperationID: "create-backup", Method: http.MethodPost, Path: "/backups",
		Summary: "Capture a complete recovery archive", MaxBodyBytes: 16384,
	}, func(ctx context.Context, input *struct{ Body BackupRequest }) (*struct{ Body BackupResult }, error) {
		request := input.Body
		snapshot, err := backups.Create(ctx, owners.Principal{Hub: request.Hub, UserID: request.UserID}, request.Repository, request.Tag)
		if errors.Is(err, errs.ErrPermissionDenied) {
			return nil, huma.Error403Forbidden(err.Error())
		}
		if errors.Is(err, errs.ErrInvalidArgument) {
			return nil, huma.Error400BadRequest(err.Error())
		}
		out := BackupResult{Snapshot: snapshot}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body BackupResult }{out}, nil
	})
}

func CreateBackup(ctx context.Context, dbPath, version string, request BackupRequest) (content.BackupSnapshot, error) {
	var out BackupResult
	err := call(ctx, dbPath, version, "/backups", request, &out, "list and verify the backup repository before retrying")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out.Snapshot, err
}

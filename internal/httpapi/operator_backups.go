package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
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

func registerOperatorBackups(api huma.API, deps *OperatorDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "create-backup", Method: http.MethodPost, Path: "/api/v1/operator/backups",
		Tags: []string{"operator"}, Security: []map[string][]string{{"localOperator": {}}},
		Summary: "Capture a complete recovery archive", MaxBodyBytes: 16384,
	}, func(ctx context.Context, input *struct{ Body BackupRequest }) (*struct{ Body BackupResult }, error) {
		if deps == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		request := input.Body
		snapshot, err := deps.Backups.Create(ctx, owners.Principal{Hub: request.Hub, UserID: request.UserID}, request.Repository, request.Tag)
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

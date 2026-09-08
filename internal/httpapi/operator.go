package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
)

// OperatorDeps is supplied only to the listener that verifies the local
// operator credential. The photo listener publishes the same contract but
// cannot execute operator operations, even for the configured photo owner.
type OperatorDeps struct {
	Owner     owners.Principal
	Checkouts *service.CheckoutService
	Backups   *service.BackupService
	Imports   *service.ImportService
}

type CheckoutCommitResult struct {
	CheckoutID string `json:"checkout_id"`
	Pending    int    `json:"pending"`
	Committed  int    `json:"committed"`
	Conflicts  int    `json:"conflicts"`
	Error      string `json:"error,omitempty"`
}

type CheckoutCommitRequest struct {
	Hub    string `json:"hub"`
	UserID string `json:"user_id"`
}

func registerOperator(api huma.API, deps *OperatorDeps) {
	api.OpenAPI().Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"localOperator": {Type: "http", Scheme: "bearer", Description: "Local operator credential, accepted only on the authenticated daemon listener. Photo-user authentication does not grant this capability."},
	}
	registerOperatorCheckouts(api, deps)
	registerOperatorInspection(api, deps)
	registerOperatorBackups(api, deps)
	registerOperatorImports(api, deps)
	registerOperatorRecovery(api, deps)
	huma.Register(api, huma.Operation{
		OperationID: "commit-checkout", Method: http.MethodPost,
		Path: "/api/v1/operator/checkouts/{id}/commit", Summary: "Commit settled tracked edits",
		Tags: []string{"operator"}, Security: []map[string][]string{{"localOperator": {}}}, MaxBodyBytes: 4096,
	}, func(ctx context.Context, input *struct {
		CheckoutID string `path:"id"`
		Body       CheckoutCommitRequest
	}) (*struct{ Body CheckoutCommitResult }, error) {
		if deps == nil || input.Body.Hub != deps.Owner.Hub || input.Body.UserID != deps.Owner.UserID {
			return nil, huma.Error403Forbidden("local operator authentication and configured owner required")
		}
		result, err := deps.Checkouts.Commit(ctx, deps.Owner, input.CheckoutID)
		out := CheckoutCommitResult{CheckoutID: input.CheckoutID, Pending: result.Pending, Committed: result.Committed, Conflicts: result.Conflicts}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body CheckoutCommitResult }{out}, nil
	})
}

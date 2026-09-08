package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/service"
)

type ContentRecoveryRequest struct {
	Hub    string `json:"hub"`
	UserID string `json:"user_id"`
	Wait   string `json:"wait,omitempty" default:"0s" doc:"Maximum wait for the shared import lock, e.g. 30s."`
}

type ContentRecoveryResult struct {
	Reports []service.OwnerRecoveryReport `json:"reports"`
	Error   string                        `json:"error,omitempty"`
}

func registerOperatorRecovery(api huma.API, deps *OperatorDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "recover-content", Method: http.MethodPost, Path: "/api/v1/operator/content/recover",
		Summary:     "Finish interrupted imports across all owners",
		Description: "Adopts matching stored files and reports unmatched content without deleting it. Results include partial per-owner counts and an error if recovery could not finish.",
		Tags:        []string{"operator"}, Security: []map[string][]string{{"localOperator": {}}}, MaxBodyBytes: 4096,
	}, func(ctx context.Context, input *struct{ Body ContentRecoveryRequest }) (*struct{ Body ContentRecoveryResult }, error) {
		if deps == nil || input.Body.Hub != deps.Owner.Hub || input.Body.UserID != deps.Owner.UserID {
			return nil, huma.Error403Forbidden("local operator authentication and configured owner required")
		}
		wait, err := time.ParseDuration(input.Body.Wait)
		if err != nil || wait < 0 {
			return nil, huma.Error400BadRequest("wait must be a non-negative duration")
		}
		reports, err := deps.Imports.Recover(ctx, deps.Owner, wait)
		out := ContentRecoveryResult{Reports: reports}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body ContentRecoveryResult }{out}, nil
	})
}

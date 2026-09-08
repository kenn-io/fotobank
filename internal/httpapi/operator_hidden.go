package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/owners"
)

type HiddenResetOperatorDeps struct {
	Service      *hidden.Service
	DefaultOwner *owners.Principal
}

type ResetHiddenRequest struct {
	Owner   string `json:"owner,omitempty" doc:"Target hub:user; defaults to the configured stub owner."`
	Confirm bool   `json:"confirm" doc:"Must be true to remove the credential and revoke sessions. Hidden flags are preserved."`
}

type ResetHiddenResult struct {
	Owner string `json:"owner"`
}

func registerOperatorHidden(api huma.API, deps *HiddenResetOperatorDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "reset-hidden-passcode", Method: http.MethodPost, Path: "/api/v1/operator/hidden/reset",
		Summary: "Reset a hidden-media passcode", Tags: []string{"operator"},
		Security:    []map[string][]string{{"localOperator": {}}},
		Description: "Host-operator recovery: removes the credential and revokes unlock sessions without revealing hidden media. Requires explicit confirmation and an owner in header mode.",
	}, func(ctx context.Context, in *struct{ Body ResetHiddenRequest }) (*struct{ Body ResetHiddenResult }, error) {
		if deps == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		if !in.Body.Confirm {
			return nil, huma.Error400BadRequest("confirm must be true")
		}
		var owner owners.Principal
		if in.Body.Owner != "" {
			hub, user, ok := strings.Cut(in.Body.Owner, ":")
			if !ok || hub == "" || user == "" {
				return nil, huma.Error400BadRequest("owner must be hub:user")
			}
			owner = owners.Principal{Hub: hub, UserID: user}
		} else if deps.DefaultOwner != nil {
			owner = *deps.DefaultOwner
		} else {
			return nil, huma.Error400BadRequest("header mode requires owner")
		}
		if err := deps.Service.AdminReset(ctx, owner); err != nil {
			return nil, Translate(err)
		}
		return &struct{ Body ResetHiddenResult }{ResetHiddenResult{Owner: owner.String()}}, nil
	})
}

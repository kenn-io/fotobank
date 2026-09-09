package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
)

type GenerationOperatorDeps struct {
	Service *aiservice.GenerationAdmin
	Owner   owners.Principal
}

type GenerationListRequest struct {
	State string `query:"state" enum:"building,active,retired"`
}
type GenerationListResult struct {
	Items []aiservice.GenerationInfo `json:"items"`
}
type GenerationDetails = aiservice.GenerationDetails
type GenerationIDRequest struct {
	ID int64 `path:"id" minimum:"1"`
}
type PromoteGenerationRequest struct {
	Confirm bool `json:"confirm"`
}
type PromoteGenerationResult struct {
	ID          int64  `json:"id"`
	Fingerprint string `json:"fingerprint"`
}
type CompactGenerationsRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}
type CompactGenerationsResult struct {
	Candidates []embedding.CompactCandidate `json:"candidates"`
	Dropped    int                          `json:"dropped"`
	Error      string                       `json:"error,omitempty"`
}

func registerOperatorGenerations(api huma.API, deps *GenerationOperatorDeps) {
	const path = "/api/v1/operator/ai/generations"
	denied := func() error {
		return huma.Error403Forbidden("embedding administration requires a local operator in stub mode")
	}
	huma.Register(api, huma.Operation{
		OperationID: "list-embedding-generations", Method: http.MethodGet, Path: path,
		Summary: "List embedding generations", Tags: []string{"operator"},
		Description: "Available only to local host operators in stub identity mode.",
		Security:    []map[string][]string{{"localOperator": {}}},
	}, func(ctx context.Context, in *GenerationListRequest) (*struct{ Body GenerationListResult }, error) {
		if deps == nil {
			return nil, denied()
		}
		rows, err := deps.Service.List(ctx, deps.Owner, in.State)
		if err != nil {
			return nil, Translate(err)
		}
		return &struct{ Body GenerationListResult }{GenerationListResult{rows}}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-embedding-generation", Method: http.MethodGet, Path: path + "/{id}",
		Summary: "Inspect an embedding generation before promotion", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}},
	}, func(ctx context.Context, in *GenerationIDRequest) (*struct{ Body GenerationDetails }, error) {
		if deps == nil {
			return nil, denied()
		}
		result, err := deps.Service.Get(ctx, deps.Owner, in.ID)
		if err != nil {
			return nil, Translate(err)
		}
		return &struct{ Body GenerationDetails }{result}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "promote-embedding-generation", Method: http.MethodPost, Path: path + "/{id}/promote",
		Summary: "Make a retired embedding generation active", Tags: []string{"operator"}, MaxBodyBytes: 4096,
		Description: "Stub-mode host operators only. Requires confirmation. The target must still be retired when the write occurs.",
		Security:    []map[string][]string{{"localOperator": {}}},
	}, func(ctx context.Context, in *struct {
		GenerationIDRequest
		Body PromoteGenerationRequest
	}) (*struct{ Body PromoteGenerationResult }, error) {
		if deps == nil {
			return nil, denied()
		}
		if !in.Body.Confirm {
			return nil, huma.Error400BadRequest("confirmation required")
		}
		row, err := deps.Service.Promote(ctx, deps.Owner, in.ID)
		if errors.Is(err, errs.ErrInvalidArgument) {
			return nil, huma.Error400BadRequest(err.Error())
		}
		if err != nil {
			return nil, Translate(err)
		}
		return &struct{ Body PromoteGenerationResult }{PromoteGenerationResult{row.ID, row.Fingerprint}}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "compact-embedding-generations", Method: http.MethodPost, Path: path + "/compact",
		Summary: "Remove retired embedding generations past the retention window", Tags: []string{"operator"}, MaxBodyBytes: 4096,
		Description: "Stub-mode host operators only. dry_run lists candidates without deleting them. On partial failure, dropped reports completed deletions and error explains the failure; inspect before retrying.",
		Security:    []map[string][]string{{"localOperator": {}}},
	}, func(ctx context.Context, in *struct{ Body CompactGenerationsRequest }) (*struct{ Body CompactGenerationsResult }, error) {
		if deps == nil {
			return nil, denied()
		}
		rows, n, err := deps.Service.Compact(ctx, deps.Owner, in.Body.DryRun)
		out := CompactGenerationsResult{Candidates: rows, Dropped: n}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body CompactGenerationsResult }{out}, nil
	})
}

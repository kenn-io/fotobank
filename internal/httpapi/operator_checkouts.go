package httpapi

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/checkout"
)

type CheckoutEstimateRequest struct {
	Hub       string             `json:"hub"`
	UserID    string             `json:"user_id"`
	Selection checkout.Selection `json:"selection"`
}

type CheckoutCreateRequest struct {
	Hub       string             `json:"hub"`
	UserID    string             `json:"user_id"`
	Selection checkout.Selection `json:"selection"`
	Root      string             `json:"root"`
	MaxBytes  int64              `json:"max_bytes"`
}

type CheckoutEstimateResult struct {
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	Error string `json:"error,omitempty"`
}

type CheckoutCreateResult struct {
	CheckoutID   string `json:"checkout_id,omitempty"`
	Root         string `json:"root"`
	Files        int    `json:"files"`
	Bytes        int64  `json:"bytes"`
	Materialized int    `json:"materialized"`
	Error        string `json:"error,omitempty"`
}

func registerOperatorCheckouts(api huma.API, deps *OperatorDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "estimate-checkout", Method: http.MethodPost, Path: "/api/v1/operator/checkouts/estimate",
		Tags: []string{"operator"}, Security: []map[string][]string{{"localOperator": {}}},
		Summary: "Estimate selected checkout files", MaxBodyBytes: 1 << 20,
	}, func(ctx context.Context, input *struct{ Body CheckoutEstimateRequest }) (*struct{ Body CheckoutEstimateResult }, error) {
		if deps == nil || input.Body.Hub != deps.Owner.Hub || input.Body.UserID != deps.Owner.UserID {
			return nil, huma.Error403Forbidden("configured owner does not match the running server")
		}
		result, err := deps.Checkouts.Estimate(ctx, deps.Owner, input.Body.Selection)
		out := CheckoutEstimateResult{Files: result.Files, Bytes: result.Bytes}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body CheckoutEstimateResult }{out}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "create-checkout", Method: http.MethodPost, Path: "/api/v1/operator/checkouts",
		Tags: []string{"operator"}, Security: []map[string][]string{{"localOperator": {}}},
		Summary: "Create a writable checkout", MaxBodyBytes: 1 << 20,
	}, func(ctx context.Context, input *struct{ Body CheckoutCreateRequest }) (*struct{ Body CheckoutCreateResult }, error) {
		if deps == nil || input.Body.Hub != deps.Owner.Hub || input.Body.UserID != deps.Owner.UserID {
			return nil, huma.Error403Forbidden("configured owner does not match the running server")
		}
		if !filepath.IsAbs(input.Body.Root) {
			return nil, huma.Error400BadRequest("checkout root must be an absolute path on the server host")
		}
		result, err := deps.Checkouts.CreateAt(ctx, deps.Owner, input.Body.Root, input.Body.Selection, input.Body.MaxBytes)
		out := CheckoutCreateResult{
			CheckoutID: result.Checkout.ID, Root: input.Body.Root,
			Files: result.Estimate.Files, Bytes: result.Estimate.Bytes, Materialized: result.Materialized,
		}
		if result.Checkout.Root != "" {
			out.Root = result.Checkout.Root
		}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body CheckoutCreateResult }{out}, nil
	})
}

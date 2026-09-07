package operator

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
)

type EstimateRequest struct {
	Hub       string             `json:"hub"`
	UserID    string             `json:"user_id"`
	Selection checkout.Selection `json:"selection"`
}

type CreateRequest struct {
	Hub       string             `json:"hub"`
	UserID    string             `json:"user_id"`
	Selection checkout.Selection `json:"selection"`
	Root      string             `json:"root"`
	MaxBytes  int64              `json:"max_bytes"`
}

type EstimateResult struct {
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	Error string `json:"error,omitempty"`
}

type CreateResult struct {
	CheckoutID   string `json:"checkout_id,omitempty"`
	Root         string `json:"root"`
	Files        int    `json:"files"`
	Bytes        int64  `json:"bytes"`
	Materialized int    `json:"materialized"`
	Error        string `json:"error,omitempty"`
}

func registerCheckouts(api huma.API, owner owners.Principal, checkouts *service.CheckoutService) {
	huma.Register(api, huma.Operation{
		OperationID: "estimate-checkout", Method: http.MethodPost, Path: "/checkouts/estimate",
		Summary: "Estimate selected checkout files", MaxBodyBytes: 1 << 20,
	}, func(ctx context.Context, input *struct{ Body EstimateRequest }) (*struct{ Body EstimateResult }, error) {
		if input.Body.Hub != owner.Hub || input.Body.UserID != owner.UserID {
			return nil, huma.Error403Forbidden("configured owner does not match the running server")
		}
		result, err := checkouts.Estimate(ctx, owner, input.Body.Selection)
		out := EstimateResult{Files: result.Files, Bytes: result.Bytes}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body EstimateResult }{out}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "create-checkout", Method: http.MethodPost, Path: "/checkouts",
		Summary: "Create a writable checkout", MaxBodyBytes: 1 << 20,
	}, func(ctx context.Context, input *struct{ Body CreateRequest }) (*struct{ Body CreateResult }, error) {
		if input.Body.Hub != owner.Hub || input.Body.UserID != owner.UserID {
			return nil, huma.Error403Forbidden("configured owner does not match the running server")
		}
		if !filepath.IsAbs(input.Body.Root) {
			return nil, huma.Error400BadRequest("checkout root must be an absolute path on the server host")
		}
		result, err := checkouts.CreateAt(ctx, owner, input.Body.Root, input.Body.Selection, input.Body.MaxBytes)
		out := CreateResult{
			CheckoutID: result.Checkout.ID, Root: input.Body.Root,
			Files: result.Estimate.Files, Bytes: result.Estimate.Bytes, Materialized: result.Materialized,
		}
		if result.Checkout.Root != "" {
			out.Root = result.Checkout.Root
		}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body CreateResult }{out}, nil
	})
}

func Estimate(ctx context.Context, dbPath, version string, request EstimateRequest) (EstimateResult, error) {
	var out EstimateResult
	err := call(ctx, dbPath, version, "/checkouts/estimate", request, &out, "retry the estimate")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}

func Create(ctx context.Context, dbPath, version string, request CreateRequest) (CreateResult, error) {
	out := CreateResult{Root: request.Root}
	err := call(ctx, dbPath, version, "/checkouts", request, &out, "inspect checkout list/status before retrying")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}

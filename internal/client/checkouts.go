package client

import (
	"context"
	"errors"
	"go.kenn.io/fotobank/internal/httpapi"
	"net/http"
)

func Estimate(ctx context.Context, configPath, version string, request httpapi.CheckoutEstimateRequest) (httpapi.CheckoutEstimateResult, error) {
	var out httpapi.CheckoutEstimateResult
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/operator/checkouts/estimate", request, &out, "retry the estimate")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}

func Create(ctx context.Context, configPath, version string, request httpapi.CheckoutCreateRequest) (httpapi.CheckoutCreateResult, error) {
	out := httpapi.CheckoutCreateResult{Root: request.Root}
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/operator/checkouts", request, &out, "inspect checkout list/status before retrying")
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}

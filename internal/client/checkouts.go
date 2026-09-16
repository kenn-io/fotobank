package client

import (
	"context"
	"errors"
	"uuid"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func RetireCheckout(ctx context.Context, configPath, version string, id uuid.UUID, request httpapi.CheckoutRetireRequest) (httpapi.CheckoutStatusOutput, error) {
	var out httpapi.CheckoutStatusOutput
	err := call(ctx, configPath, version, &out, "inspect checkout status before retrying retirement", func(c *generated.Client) (*generated.RetireCheckoutResponse, error) {
		return c.RetireCheckout(ctx, &generated.RetireCheckoutRequestOptions{Body: &request, PathParams: &generated.RetireCheckoutPath{ID: id.String()}})
	})
	return out, err
}

func Estimate(ctx context.Context, configPath, version string, request httpapi.CheckoutEstimateRequest) (httpapi.CheckoutEstimateResult, error) {
	var out httpapi.CheckoutEstimateResult
	err := call(ctx, configPath, version, &out, "retry the estimate", func(c *generated.Client) (*generated.EstimateCheckoutResponse, error) {
		return c.EstimateCheckout(ctx, &generated.EstimateCheckoutRequestOptions{Body: &request})
	})
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}

func Create(ctx context.Context, configPath, version string, request httpapi.CheckoutCreateRequest) (httpapi.CheckoutCreateResult, error) {
	out := httpapi.CheckoutCreateResult{Root: request.Root}
	err := call(ctx, configPath, version, &out, "inspect checkout list/status before retrying", func(c *generated.Client) (*generated.CreateCheckoutResponse, error) {
		return c.CreateCheckout(ctx, &generated.CreateCheckoutRequestOptions{Body: &request})
	})
	if err == nil && out.Error != "" {
		err = errors.New(out.Error)
	}
	return out, err
}

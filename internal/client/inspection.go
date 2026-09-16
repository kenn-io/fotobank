package client

import (
	"context"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
)

func ListCheckouts(ctx context.Context, configPath, version string, owner owners.Principal) ([]httpapi.CheckoutSummaryOutput, error) {
	var out generated.ListCheckoutsResponse
	err := call(ctx, configPath, version, &out, "retry the inspection", func(c *generated.Client) (*generated.ListCheckoutsResponse, error) {
		return c.ListCheckouts(ctx, &generated.ListCheckoutsRequestOptions{Query: &generated.ListCheckoutsQuery{Hub: new(owner.Hub), UserID: new(owner.UserID)}})
	})
	return out, err
}
func CheckoutStatus(ctx context.Context, configPath, version, checkoutID string, owner owners.Principal) (httpapi.CheckoutStatusOutput, error) {
	var out httpapi.CheckoutStatusOutput
	err := call(ctx, configPath, version, &out, "retry the inspection", func(c *generated.Client) (*generated.CheckoutStatusResponse, error) {
		return c.CheckoutStatus(ctx, &generated.CheckoutStatusRequestOptions{PathParams: &generated.CheckoutStatusPath{CheckoutID: checkoutID}, Query: &generated.CheckoutStatusQuery{Hub: new(owner.Hub), UserID: new(owner.UserID)}})
	})
	return out, err
}

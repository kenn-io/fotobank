package client

import (
	"context"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
	"net/http"
	"net/url"
)

func ListCheckouts(ctx context.Context, configPath, version string, owner owners.Principal) ([]httpapi.CheckoutSummaryOutput, error) {
	var out []httpapi.CheckoutSummaryOutput
	query := url.Values{"hub": {owner.Hub}, "user_id": {owner.UserID}}
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/operator/checkouts?"+query.Encode(), nil, &out, "retry the inspection")
	return out, err
}
func CheckoutStatus(ctx context.Context, configPath, version, checkoutID string, owner owners.Principal) (httpapi.CheckoutStatusOutput, error) {
	var out httpapi.CheckoutStatusOutput
	query := url.Values{"hub": {owner.Hub}, "user_id": {owner.UserID}}
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/operator/checkouts/"+url.PathEscape(checkoutID)+"?"+query.Encode(), nil, &out, "retry the inspection")
	return out, err
}

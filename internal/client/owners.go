package client

import (
	"context"
	"net/http"
	"net/url"

	"go.kenn.io/fotobank/internal/httpapi"
)

func RegisterOwner(ctx context.Context, configPath, version string, request httpapi.RegisterOwnerRequest) (httpapi.OwnerResult, error) {
	var result httpapi.OwnerResult
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/operator/owners", request, &result, "list owners before retrying registration")
	return result, err
}

func ListOwners(ctx context.Context, configPath, version string) (httpapi.OwnerListResult, error) {
	var result httpapi.OwnerListResult
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/operator/owners", nil, &result, "retry owner listing")
	return result, err
}

func RemoveOwner(ctx context.Context, configPath, version string, request httpapi.RemoveOwnerRequest) error {
	query := url.Values{"hub": {request.Hub}, "user_id": {request.UserID}}
	return call(ctx, configPath, version, http.MethodDelete, "/api/v1/operator/owners?"+query.Encode(), nil, nil, "list owners before retrying removal")
}

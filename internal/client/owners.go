package client

import (
	"context"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func RegisterOwner(ctx context.Context, configPath, version string, request httpapi.RegisterOwnerRequest) (httpapi.OwnerResult, error) {
	var result httpapi.OwnerResult
	err := call(ctx, configPath, version, &result, "list owners before retrying registration", func(c *generated.Client) (*generated.RegisterOwnerResponse, error) {
		return c.RegisterOwner(ctx, &generated.RegisterOwnerRequestOptions{Body: &request})
	})
	return result, err
}

func ListOwners(ctx context.Context, configPath, version string) (httpapi.OwnerListResult, error) {
	var result httpapi.OwnerListResult
	err := call(ctx, configPath, version, &result, "retry owner listing", func(c *generated.Client) (*generated.ListOwnersResponse, error) {
		return c.ListOwners(ctx)
	})
	return result, err
}

func RemoveOwner(ctx context.Context, configPath, version string, request httpapi.RemoveOwnerRequest) error {
	return call(ctx, configPath, version, nil, "list owners before retrying removal", func(c *generated.Client) (*struct{}, error) {
		return c.RemoveOwner(ctx, &generated.RemoveOwnerRequestOptions{Query: &generated.RemoveOwnerQuery{Hub: request.Hub, UserID: request.UserID}})
	})
}

package client

import (
	"context"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func CreateShare(ctx context.Context, configPath, version string, input httpapi.CreateShareRequest) (httpapi.ScopeDTO, error) {
	var out httpapi.ScopeDTO
	err := call(ctx, configPath, version, &out, "list shares before retrying creation", func(c *generated.Client) (*generated.SharesCreateResponse, error) {
		return c.SharesCreate(ctx, &generated.SharesCreateRequestOptions{Body: &input})
	})
	return out, err
}

func ListShares(ctx context.Context, configPath, version string, input httpapi.ListSharesInput) (httpapi.ShareListResult, error) {
	var out httpapi.ShareListResult
	err := call(ctx, configPath, version, &out, "retry share listing", func(c *generated.Client) (*generated.SharesListResponse, error) {
		return c.SharesList(ctx, &generated.SharesListRequestOptions{Query: &generated.SharesListQuery{AlbumID: new(input.AlbumID), GranteeHub: new(input.GranteeHub), GranteeUserID: new(input.GranteeUserID), Status: new(input.Status), IncludeSettled: new(input.IncludeSettled), Limit: new(int64(input.Limit)), Offset: new(int64(input.Offset))}})
	})
	return out, err
}

func GetShare(ctx context.Context, configPath, version, id string) (httpapi.ScopeDTO, error) {
	var out httpapi.ScopeDTO
	err := call(ctx, configPath, version, &out, "retry share inspection", func(c *generated.Client) (*generated.SharesGetResponse, error) {
		return c.SharesGet(ctx, &generated.SharesGetRequestOptions{PathParams: &generated.SharesGetPath{UUID: id}})
	})
	return out, err
}

func RevokeShare(ctx context.Context, configPath, version, id string) (httpapi.ScopeDTO, error) {
	var out httpapi.ScopeDTO
	err := call(ctx, configPath, version, &out, "inspect the share before retrying revocation", func(c *generated.Client) (*generated.SharesRevokeResponse, error) {
		return c.SharesRevoke(ctx, &generated.SharesRevokeRequestOptions{PathParams: &generated.SharesRevokePath{UUID: id}})
	})
	return out, err
}

func RetryShare(ctx context.Context, configPath, version, id string) (httpapi.ScopeDTO, error) {
	var out httpapi.ScopeDTO
	err := call(ctx, configPath, version, &out, "inspect the share before retrying", func(c *generated.Client) (*generated.SharesRetryResponse, error) {
		return c.SharesRetry(ctx, &generated.SharesRetryRequestOptions{PathParams: &generated.SharesRetryPath{UUID: id}})
	})
	return out, err
}

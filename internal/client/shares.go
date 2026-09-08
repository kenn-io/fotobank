package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"go.kenn.io/fotobank/internal/httpapi"
)

func CreateShare(ctx context.Context, dbPath, version string, input httpapi.CreateShareRequest) (httpapi.ScopeDTO, error) {
	var out httpapi.ScopeDTO
	err := call(ctx, dbPath, version, http.MethodPost, "/api/v1/shares", input, &out, "list shares before retrying creation")
	return out, err
}

func ListShares(ctx context.Context, dbPath, version string, input httpapi.ListSharesInput) (httpapi.ShareListResult, error) {
	var out httpapi.ShareListResult
	query := url.Values{
		"album_id": {input.AlbumID}, "grantee_hub": {input.GranteeHub}, "grantee_user_id": {input.GranteeUserID},
		"status": {input.Status}, "include_settled": {strconv.FormatBool(input.IncludeSettled)},
		"limit": {strconv.Itoa(input.Limit)}, "offset": {strconv.Itoa(input.Offset)},
	}
	err := call(ctx, dbPath, version, http.MethodGet, "/api/v1/shares?"+query.Encode(), nil, &out, "retry share listing")
	return out, err
}

func GetShare(ctx context.Context, dbPath, version, id string) (httpapi.ScopeDTO, error) {
	var out httpapi.ScopeDTO
	err := call(ctx, dbPath, version, http.MethodGet, "/api/v1/shares/"+url.PathEscape(id), nil, &out, "retry share inspection")
	return out, err
}

func RevokeShare(ctx context.Context, dbPath, version, id string) (httpapi.ScopeDTO, error) {
	var out httpapi.ScopeDTO
	err := call(ctx, dbPath, version, http.MethodPost, "/api/v1/shares/"+url.PathEscape(id)+"/revoke", nil, &out, "inspect the share before retrying revocation")
	return out, err
}

func RetryShare(ctx context.Context, dbPath, version, id string) (httpapi.ScopeDTO, error) {
	var out httpapi.ScopeDTO
	err := call(ctx, dbPath, version, http.MethodPost, "/api/v1/shares/"+url.PathEscape(id)+"/retry", nil, &out, "inspect the share before retrying")
	return out, err
}

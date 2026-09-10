package client

import (
	"context"
	"net/http"

	"go.kenn.io/fotobank/internal/httpapi"
)

func RegenerateThumbs(ctx context.Context, configPath, version string, request httpapi.RegenerateThumbsRequest) (httpapi.RegenerateThumbsResult, error) {
	var result httpapi.RegenerateThumbsResult
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/operator/thumbs/regenerate", request, &result, "inspect thumbnail status before retrying regeneration")
	return result, err
}

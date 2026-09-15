package client

import (
	"context"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func RegenerateThumbs(ctx context.Context, configPath, version string, request httpapi.RegenerateThumbsRequest) (httpapi.RegenerateThumbsResult, error) {
	var result httpapi.RegenerateThumbsResult
	err := call(ctx, configPath, version, &result, "inspect thumbnail status before retrying regeneration", func(c *generated.Client) (*generated.RegenerateThumbnailsResponse, error) {
		return c.RegenerateThumbnails(ctx, &generated.RegenerateThumbnailsRequestOptions{Body: &request})
	})
	return result, err
}

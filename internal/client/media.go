package client

import (
	"context"
	"uuid"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func ListMedia(ctx context.Context, configPath, version string, input httpapi.ListMediaInput) (httpapi.MediaListResult, error) {
	query := generated.ListMediaQuery{
		Limit: new(int64(input.Limit)), Offset: new(int64(input.Offset)), SortDesc: new(input.SortDesc),
		Camera: input.Camera, Lens: input.Lens, FacetTag: input.FacetTag,
	}
	if input.MediaType != "" {
		query.MediaType = new(input.MediaType)
	}
	if input.HasGPS != "" {
		query.HasGps = new(generated.ListMediaQueryHasGps(input.HasGPS))
	}
	var out httpapi.MediaListResult
	err := call(ctx, configPath, version, &out, "retry media listing", func(c *generated.Client) (*generated.ListMediaResponse, error) {
		return c.ListMedia(ctx, &generated.ListMediaRequestOptions{Query: &query})
	})
	return out, err
}

func GetMedia(ctx context.Context, configPath, version string, id uuid.UUID) (httpapi.MediaDTO, error) {
	var out httpapi.MediaDTO
	err := call(ctx, configPath, version, &out, "retry media inspection", func(c *generated.Client) (*generated.GetMediaResponse, error) {
		return c.GetMedia(ctx, &generated.GetMediaRequestOptions{PathParams: &generated.GetMediaPath{ID: id.String()}})
	})
	return out, err
}

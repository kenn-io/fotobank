package client

import (
	"context"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func SearchMedia(ctx context.Context, configPath, version string, input httpapi.SearchInput) (httpapi.SearchBody, error) {
	query := generated.SearchQuery{
		Q: new(input.Q), Limit: new(int64(input.Limit)), Tag: input.Tag, Camera: input.Camera, Lens: input.Lens, FacetTag: input.FacetTag,
		IncludeHidden: new(input.IncludeHidden), Explain: new(input.Explain),
	}
	if input.Sort != "" {
		query.Sort = new(generated.SearchQuerySort(input.Sort))
	}
	if input.Cursor != "" {
		query.Cursor = new(input.Cursor)
	}
	if input.MediaType != "" {
		query.MediaType = new(generated.SearchQueryMediaType(input.MediaType))
	}
	if input.Location != "" {
		query.Location = new(input.Location)
	}
	if input.HasGPS != "" {
		query.HasGps = new(generated.SearchQueryHasGps(input.HasGPS))
	}
	if !input.DateAfter.IsZero() {
		query.DateAfter = new(input.DateAfter)
	}
	if !input.DateBefore.IsZero() {
		query.DateBefore = new(input.DateBefore)
	}
	var out httpapi.SearchBody
	err := call(ctx, configPath, version, &out, "retry search; omit --cursor to restart pagination", func(c *generated.Client) (*generated.SearchResponse, error) {
		return c.Search(ctx, &generated.SearchRequestOptions{Query: &query})
	})
	return out, err
}

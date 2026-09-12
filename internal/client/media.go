package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"uuid"

	"go.kenn.io/fotobank/internal/httpapi"
)

func ListMedia(ctx context.Context, configPath, version string, input httpapi.ListMediaInput) (httpapi.MediaListResult, error) {
	query := url.Values{
		"limit": {strconv.Itoa(input.Limit)}, "offset": {strconv.Itoa(input.Offset)},
		"sort_desc": {strconv.FormatBool(input.SortDesc)},
		"camera":    input.Camera, "lens": input.Lens, "facet_tag": input.FacetTag,
	}
	if input.MediaType != "" {
		query.Set("media_type", input.MediaType)
	}
	if input.HasGPS != "" {
		query.Set("has_gps", input.HasGPS)
	}
	var out httpapi.MediaListResult
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/media?"+query.Encode(), nil, &out, "retry media listing")
	return out, err
}

func GetMedia(ctx context.Context, configPath, version string, id uuid.UUID) (httpapi.MediaDTO, error) {
	var out httpapi.MediaDTO
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/media/"+url.PathEscape(id.String()), nil, &out, "retry media inspection")
	return out, err
}

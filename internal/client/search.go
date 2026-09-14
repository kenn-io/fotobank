package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.kenn.io/fotobank/internal/httpapi"
)

func SearchMedia(ctx context.Context, configPath, version string, input httpapi.SearchInput) (httpapi.SearchBody, error) {
	query := url.Values{
		"q": {input.Q}, "limit": {strconv.Itoa(input.Limit)},
		"tag": input.Tag, "camera": input.Camera, "lens": input.Lens, "facet_tag": input.FacetTag,
		"include_hidden": {strconv.FormatBool(input.IncludeHidden)}, "explain": {strconv.FormatBool(input.Explain)},
	}
	for key, value := range map[string]string{
		"sort": input.Sort, "cursor": input.Cursor, "media_type": input.MediaType,
		"location": input.Location, "has_gps": input.HasGPS,
	} {
		if value != "" {
			query.Set(key, value)
		}
	}
	if !input.DateAfter.IsZero() {
		query.Set("date_after", input.DateAfter.Format(time.RFC3339Nano))
	}
	if !input.DateBefore.IsZero() {
		query.Set("date_before", input.DateBefore.Format(time.RFC3339Nano))
	}
	var out httpapi.SearchBody
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/search?"+query.Encode(), nil, &out, "retry search; omit --cursor to restart pagination")
	return out, err
}

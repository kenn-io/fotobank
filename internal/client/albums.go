package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"go.kenn.io/fotobank/internal/httpapi"
)

func CreateAlbum(ctx context.Context, configPath, version string, input httpapi.AlbumNameRequest) (httpapi.AlbumDTO, error) {
	var out httpapi.AlbumDTO
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/albums", input, &out, "list albums before retrying creation")
	return out, err
}

func RenameAlbum(ctx context.Context, configPath, version, id string, input httpapi.AlbumNameRequest) (httpapi.AlbumDTO, error) {
	var out httpapi.AlbumDTO
	err := call(ctx, configPath, version, http.MethodPatch, "/api/v1/albums/"+url.PathEscape(id), input, &out, "inspect the album before retrying")
	return out, err
}

func DeleteAlbum(ctx context.Context, configPath, version, id string) error {
	return call(ctx, configPath, version, http.MethodDelete, "/api/v1/albums/"+url.PathEscape(id), nil, nil, "list albums before retrying deletion")
}

func GetAlbum(ctx context.Context, configPath, version, id string) (httpapi.AlbumDTO, error) {
	var out httpapi.AlbumDTO
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/albums/"+url.PathEscape(id), nil, &out, "retry album inspection")
	return out, err
}

func ListAlbums(ctx context.Context, configPath, version string, input httpapi.ListAlbumsInput) (httpapi.AlbumListResult, error) {
	var out httpapi.AlbumListResult
	query := url.Values{"limit": {strconv.Itoa(input.Limit)}, "offset": {strconv.Itoa(input.Offset)}}
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/albums?"+query.Encode(), nil, &out, "retry album listing")
	return out, err
}

func ListAlbumMedia(ctx context.Context, configPath, version string, input httpapi.ListAlbumMediaInput) (httpapi.AlbumMediaResult, error) {
	var out httpapi.AlbumMediaResult
	query := url.Values{
		"limit": {strconv.Itoa(input.Limit)}, "offset": {strconv.Itoa(input.Offset)},
		"sort_by": {input.SortBy}, "sort_asc": {strconv.FormatBool(input.SortAsc)},
	}
	err := call(ctx, configPath, version, http.MethodGet, "/api/v1/albums/"+url.PathEscape(input.AlbumID)+"/media?"+query.Encode(), nil, &out, "retry album inspection")
	return out, err
}

func AddAlbumMedia(ctx context.Context, configPath, version, id string, input httpapi.AddAlbumMediaRequest) (httpapi.AddAlbumMediaResult, error) {
	var out httpapi.AddAlbumMediaResult
	err := call(ctx, configPath, version, http.MethodPost, "/api/v1/albums/"+url.PathEscape(id)+"/media", input, &out, "inspect album membership before retrying")
	return out, err
}

func RemoveAlbumMedia(ctx context.Context, configPath, version, albumID, mediaID string) error {
	return call(ctx, configPath, version, http.MethodDelete,
		"/api/v1/albums/"+url.PathEscape(albumID)+"/media/"+url.PathEscape(mediaID), nil, nil, "inspect album membership before retrying")
}

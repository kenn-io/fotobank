package client

import (
	"context"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

func CreateAlbum(ctx context.Context, configPath, version string, input httpapi.AlbumNameRequest) (httpapi.AlbumDTO, error) {
	var out httpapi.AlbumDTO
	err := call(ctx, configPath, version, &out, "list albums before retrying creation", func(c *generated.Client) (*generated.CreateAlbumResponse, error) {
		return c.CreateAlbum(ctx, &generated.CreateAlbumRequestOptions{Body: &input})
	})
	return out, err
}

func RenameAlbum(ctx context.Context, configPath, version, id string, input httpapi.AlbumNameRequest) (httpapi.AlbumDTO, error) {
	var out httpapi.AlbumDTO
	err := call(ctx, configPath, version, &out, "inspect the album before retrying", func(c *generated.Client) (*generated.RenameAlbumResponse, error) {
		return c.RenameAlbum(ctx, &generated.RenameAlbumRequestOptions{Body: &input, PathParams: &generated.RenameAlbumPath{ID: id}})
	})
	return out, err
}

func DeleteAlbum(ctx context.Context, configPath, version, id string) error {
	return call(ctx, configPath, version, nil, "list albums before retrying deletion", func(c *generated.Client) (*struct{}, error) {
		return c.DeleteAlbum(ctx, &generated.DeleteAlbumRequestOptions{PathParams: &generated.DeleteAlbumPath{ID: id}})
	})
}

func GetAlbum(ctx context.Context, configPath, version, id string) (httpapi.AlbumDTO, error) {
	var out httpapi.AlbumDTO
	err := call(ctx, configPath, version, &out, "retry album inspection", func(c *generated.Client) (*generated.GetAlbumResponse, error) {
		return c.GetAlbum(ctx, &generated.GetAlbumRequestOptions{PathParams: &generated.GetAlbumPath{ID: id}})
	})
	return out, err
}

func ListAlbums(ctx context.Context, configPath, version string, input httpapi.ListAlbumsInput) (httpapi.AlbumListResult, error) {
	var out httpapi.AlbumListResult
	err := call(ctx, configPath, version, &out, "retry album listing", func(c *generated.Client) (*generated.ListAlbumsResponse, error) {
		return c.ListAlbums(ctx, &generated.ListAlbumsRequestOptions{Query: &generated.ListAlbumsQuery{Limit: new(int64(input.Limit)), Offset: new(int64(input.Offset))}})
	})
	return out, err
}

func ListAlbumMedia(ctx context.Context, configPath, version string, input httpapi.ListAlbumMediaInput) (httpapi.AlbumMediaResult, error) {
	var out httpapi.AlbumMediaResult
	err := call(ctx, configPath, version, &out, "retry album inspection", func(c *generated.Client) (*generated.ListAlbumMediaResponse, error) {
		return c.ListAlbumMedia(ctx, &generated.ListAlbumMediaRequestOptions{PathParams: &generated.ListAlbumMediaPath{ID: input.AlbumID}, Query: &generated.ListAlbumMediaQuery{Limit: new(int64(input.Limit)), Offset: new(int64(input.Offset)), SortBy: new(input.SortBy), SortAsc: new(input.SortAsc)}})
	})
	return out, err
}

func AddAlbumMedia(ctx context.Context, configPath, version, id string, input httpapi.AddAlbumMediaRequest) (httpapi.AddAlbumMediaResult, error) {
	var out httpapi.AddAlbumMediaResult
	err := call(ctx, configPath, version, &out, "inspect album membership before retrying", func(c *generated.Client) (*generated.AddAlbumMediaResponse, error) {
		return c.AddAlbumMedia(ctx, &generated.AddAlbumMediaRequestOptions{Body: &input, PathParams: &generated.AddAlbumMediaPath{ID: id}})
	})
	return out, err
}

func RemoveAlbumMedia(ctx context.Context, configPath, version, albumID, mediaID string) error {
	return call(ctx, configPath, version, nil, "inspect album membership before retrying", func(c *generated.Client) (*struct{}, error) {
		return c.RemoveAlbumMedia(ctx, &generated.RemoveAlbumMediaRequestOptions{PathParams: &generated.RemoveAlbumMediaPath{ID: albumID, MediaID: mediaID}})
	})
}

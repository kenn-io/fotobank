package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/share"
)

// translateAlbumError maps service-layer errors to huma.StatusError with
// album-specific rules that differ from the global Translate:
//
//   - errs.ErrOwnerMismatch → 500. In the albums surface this means the
//     AlbumService pre-flight missed a row and the DB trigger fired.
//     The global Translate would return 403, leaking existence.
//   - album.ErrInvalidName / ErrInvalidBatch / ErrInvalidSort → 400 with
//     explicit wire strings (not the sentinels' "album: ..." prefix).
//   - everything else → delegate to the shared httpapi.Translate.
//
// Callers log the original err before returning; this wrapper only maps.
func translateAlbumError(err error) huma.StatusError {
	switch {
	case errors.Is(err, errs.ErrOwnerMismatch):
		return huma.Error500InternalServerError(http.StatusText(http.StatusInternalServerError))
	case errors.Is(err, album.ErrInvalidName):
		return huma.Error400BadRequest("name must be 1..200 chars")
	case errors.Is(err, album.ErrInvalidBatch):
		return huma.Error400BadRequest("batch size must be 1..500")
	case errors.Is(err, album.ErrInvalidSort):
		return huma.Error400BadRequest("sort_by must be added, imported, or taken")
	case errors.Is(err, share.ErrAlbumHasLiveScopes):
		return huma.Error409Conflict("album has outstanding shares; revoke or retry them first")
	default:
		return Translate(err)
	}
}

// coverDTO / albumDTO are the wire shapes for albums.
type coverDTO struct {
	MediaID      string `json:"media_id"`
	ThumbVersion int    `json:"thumb_version"`
}

type albumDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ItemCount   int       `json:"item_count"`
	HiddenCount int       `json:"hidden_count"`
	Cover       *coverDTO `json:"cover,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toAlbumDTO(it album.AlbumListItem) albumDTO {
	out := albumDTO{
		ID:          it.ID,
		Name:        it.Name,
		ItemCount:   it.ItemCount,
		HiddenCount: it.HiddenCount,
		CreatedAt:   it.CreatedAt,
		UpdatedAt:   it.UpdatedAt,
	}
	if it.Cover != nil {
		out.Cover = &coverDTO{MediaID: it.Cover.MediaID, ThumbVersion: it.Cover.ThumbVersion}
	}
	return out
}

// registerAlbums is the entry point called from buildAPI. svc may be
// nil; in that case every operation answers 503 Service Unavailable so
// the OpenAPI spec dumper can pass an empty Deps.
func registerAlbums(api huma.API, svc *service.AlbumService) {
	registerAlbumsCRUD(api, svc)
	registerAlbumMedia(api, svc)
}

const (
	albumsListDefaultLimit = 100
	albumsListMaxLimit     = 1000
)

type listAlbumsInput struct {
	Limit  int `query:"limit" doc:"max rows to return (default 100, cap 1000)"`
	Offset int `query:"offset" doc:"pagination offset"`
}

type listAlbumsOutput struct {
	Body struct {
		Items      []albumDTO `json:"items"`
		NextOffset *int       `json:"next_offset,omitempty"`
	}
}

type createAlbumInput struct {
	Body struct {
		Name string `json:"name"`
	}
}

type createAlbumOutput struct {
	Status int
	Body   albumDTO
}

type getAlbumInput struct {
	ID string `path:"id"`
}

type getAlbumOutput struct {
	Body albumDTO
}

type patchAlbumInput struct {
	ID   string `path:"id"`
	Body struct {
		Name string `json:"name"`
	}
}

type deleteAlbumInput struct {
	ID string `path:"id"`
}

type deleteAlbumOutput struct {
	Status int
}

func clampLimit(in, def, maxCap int) int {
	if in <= 0 {
		return def
	}
	if in > maxCap {
		return maxCap
	}
	return in
}

func registerAlbumsCRUD(api huma.API, svc *service.AlbumService) {
	registerListAlbums(api, svc)
	registerCreateAlbum(api, svc)
	registerGetAlbum(api, svc)
	registerRenameAlbum(api, svc)
	registerDeleteAlbum(api, svc)
}

func registerListAlbums(api huma.API, svc *service.AlbumService) {
	huma.Register(api, huma.Operation{
		OperationID: "list-albums",
		Method:      http.MethodGet,
		Path:        "/api/v1/albums",
		Summary:     "List albums belonging to the caller",
	}, func(ctx context.Context, in *listAlbumsInput) (*listAlbumsOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("album service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		limit := clampLimit(in.Limit, albumsListDefaultLimit, albumsListMaxLimit)
		offset := max(in.Offset, 0)
		rows, err := svc.List(ctx, id.Principal.OwnersPrincipal(), limit+1, offset)
		if err != nil {
			return nil, translateAlbumError(err)
		}
		out := &listAlbumsOutput{}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
			next := offset + limit
			out.Body.NextOffset = &next
		}
		out.Body.Items = make([]albumDTO, 0, len(rows))
		for _, it := range rows {
			out.Body.Items = append(out.Body.Items, toAlbumDTO(it))
		}
		return out, nil
	})
}

func registerCreateAlbum(api huma.API, svc *service.AlbumService) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-album",
		Method:        http.MethodPost,
		Path:          "/api/v1/albums",
		Summary:       "Create an album owned by the caller",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createAlbumInput) (*createAlbumOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("album service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		it, err := svc.Create(ctx, id.Principal.OwnersPrincipal(), in.Body.Name)
		if err != nil {
			return nil, translateAlbumError(err)
		}
		return &createAlbumOutput{Status: http.StatusCreated, Body: toAlbumDTO(it)}, nil
	})
}

func registerGetAlbum(api huma.API, svc *service.AlbumService) {
	huma.Register(api, huma.Operation{
		OperationID: "get-album",
		Method:      http.MethodGet,
		Path:        "/api/v1/albums/{id}",
		Summary:     "Return detail for a single album",
	}, func(ctx context.Context, in *getAlbumInput) (*getAlbumOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("album service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		it, err := svc.GetDetail(ctx, in.ID, id.Principal.OwnersPrincipal())
		if err != nil {
			return nil, translateAlbumError(err)
		}
		return &getAlbumOutput{Body: toAlbumDTO(it)}, nil
	})
}

func registerRenameAlbum(api huma.API, svc *service.AlbumService) {
	huma.Register(api, huma.Operation{
		OperationID: "rename-album",
		Method:      http.MethodPatch,
		Path:        "/api/v1/albums/{id}",
		Summary:     "Rename an album (returns the updated detail)",
	}, func(ctx context.Context, in *patchAlbumInput) (*getAlbumOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("album service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		it, err := svc.Rename(ctx, in.ID, in.Body.Name, id.Principal.OwnersPrincipal())
		if err != nil {
			return nil, translateAlbumError(err)
		}
		return &getAlbumOutput{Body: toAlbumDTO(it)}, nil
	})
}

func registerDeleteAlbum(api huma.API, svc *service.AlbumService) {
	huma.Register(api, huma.Operation{
		OperationID:   "delete-album",
		Method:        http.MethodDelete,
		Path:          "/api/v1/albums/{id}",
		Summary:       "Delete an album (cascades album_media)",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *deleteAlbumInput) (*deleteAlbumOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("album service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		if err := svc.Delete(ctx, in.ID, id.Principal.OwnersPrincipal()); err != nil {
			return nil, translateAlbumError(err)
		}
		return &deleteAlbumOutput{Status: http.StatusNoContent}, nil
	})
}

type listAlbumMediaInput struct {
	AlbumID string `path:"id"`
	Limit   int    `query:"limit" doc:"max rows to return (default 100, cap 1000)"`
	Offset  int    `query:"offset" doc:"pagination offset"`
	SortBy  string `query:"sort_by" doc:"added (default), imported, or taken"`
	SortAsc bool   `query:"sort_asc" doc:"invert the default DESC sort when true"`
}

type listAlbumMediaOutput struct {
	Body struct {
		Items      []mediaDTO `json:"items"`
		NextOffset *int       `json:"next_offset,omitempty"`
	}
}

type addAlbumMediaInput struct {
	AlbumID string `path:"id"`
	Body    struct {
		MediaIDs []string `json:"media_ids"`
	}
}

type addAlbumMediaOutput struct {
	Body struct {
		Added          int `json:"added"`
		AlreadyPresent int `json:"already_present"`
	}
}

type removeAlbumMediaInput struct {
	AlbumID string `path:"id"`
	MediaID string `path:"media_id"`
}

type removeAlbumMediaOutput struct {
	Status int
}

func registerAlbumMedia(api huma.API, svc *service.AlbumService) {
	registerListAlbumMedia(api, svc)
	registerAddAlbumMedia(api, svc)
	registerRemoveAlbumMedia(api, svc)
}

func registerListAlbumMedia(api huma.API, svc *service.AlbumService) {
	huma.Register(api, huma.Operation{
		OperationID: "list-album-media",
		Method:      http.MethodGet,
		Path:        "/api/v1/albums/{id}/media",
		Summary:     "List media in an album",
	}, func(ctx context.Context, in *listAlbumMediaInput) (*listAlbumMediaOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("album service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		limit := clampLimit(in.Limit, albumsListDefaultLimit, albumsListMaxLimit)
		offset := max(in.Offset, 0)
		filter := album.AlbumMediaFilter{
			Limit:   limit + 1,
			Offset:  offset,
			SortBy:  in.SortBy,
			SortAsc: in.SortAsc,
		}
		rows, err := svc.ListMedia(ctx, in.AlbumID, filter, id.Principal.OwnersPrincipal())
		if err != nil {
			return nil, translateAlbumError(err)
		}
		out := &listAlbumMediaOutput{}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
			next := offset + limit
			out.Body.NextOffset = &next
		}
		out.Body.Items = make([]mediaDTO, 0, len(rows))
		for _, m := range rows {
			out.Body.Items = append(out.Body.Items, toMediaDTO(m))
		}
		return out, nil
	})
}

func registerAddAlbumMedia(api huma.API, svc *service.AlbumService) {
	huma.Register(api, huma.Operation{
		OperationID: "add-album-media",
		Method:      http.MethodPost,
		Path:        "/api/v1/albums/{id}/media",
		Summary:     "Add media to an album (idempotent, deduped)",
	}, func(ctx context.Context, in *addAlbumMediaInput) (*addAlbumMediaOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("album service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		caller := id.Principal.OwnersPrincipal()
		var opts []service.AddMediaOption
		if claim, hasClaim := hidden.UnlockClaimFromContext(ctx); hasClaim && claim.Principal == caller {
			opts = append(opts, service.WithHiddenMediaAllowed())
		}
		added, already, err := svc.AddMedia(ctx, in.AlbumID, in.Body.MediaIDs, caller, opts...)
		if err != nil {
			return nil, translateAlbumError(err)
		}
		out := &addAlbumMediaOutput{}
		out.Body.Added = added
		out.Body.AlreadyPresent = already
		return out, nil
	})
}

func registerRemoveAlbumMedia(api huma.API, svc *service.AlbumService) {
	huma.Register(api, huma.Operation{
		OperationID:   "remove-album-media",
		Method:        http.MethodDelete,
		Path:          "/api/v1/albums/{id}/media/{media_id}",
		Summary:       "Remove a media row from an album",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *removeAlbumMediaInput) (*removeAlbumMediaOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("album service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		if err := svc.RemoveMedia(ctx, in.AlbumID, in.MediaID, id.Principal.OwnersPrincipal()); err != nil {
			return nil, translateAlbumError(err)
		}
		return &removeAlbumMediaOutput{Status: http.StatusNoContent}, nil
	})
}

// TranslateAlbumErrorForTest is an internal-only export so albums_test
// (in package httpapi_test) can unit-test translateAlbumError without
// promoting the symbol into the public API.
func TranslateAlbumErrorForTest(err error) huma.StatusError {
	if err == nil {
		return nil
	}
	return translateAlbumError(err)
}

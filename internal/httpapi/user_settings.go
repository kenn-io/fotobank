package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/service/usersettings"
)

// userSettingValue is the wire shape returned by GET. The body carries
// the raw JSON value that was previously stored via PUT.
type userSettingValue struct {
	Body struct {
		Value string `json:"value"`
	}
}

// userSettingPathInput is the path-only input shared by GET and DELETE.
// The pattern matches the ASCII subset documented for setting keys; the
// length cap mirrors the column limit in the user_settings migration.
type userSettingPathInput struct {
	Key string `path:"key" maxLength:"128" pattern:"^[A-Za-z0-9_.-]+$"`
}

// userSettingPutInput is the PUT payload: same path constraints plus a
// JSON body whose Value field is the opaque string the caller wants
// stored.
type userSettingPutInput struct {
	Key  string `path:"key" maxLength:"128" pattern:"^[A-Za-z0-9_.-]+$"`
	Body struct {
		Value string `json:"value"`
	}
}

// registerUserSettings mounts the three /api/v1/settings/user/{key}
// operations on api. svc may be nil; in that case every operation
// answers 503 Service Unavailable so the OpenAPI spec dumper can pass
// an empty Deps and still emit the schema.
func registerUserSettings(api huma.API, svc *usersettings.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "get-user-setting",
		Method:      http.MethodGet,
		Path:        "/api/v1/settings/user/{key}",
		Summary:     "Get a per-user UI preference",
	}, func(ctx context.Context, in *userSettingPathInput) (*userSettingValue, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("user settings service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		v, found, err := svc.Get(ctx, id.Principal.OwnersPrincipal(), in.Key)
		if err != nil {
			return nil, Translate(err)
		}
		if !found {
			return nil, Translate(errs.ErrNotFound)
		}
		out := &userSettingValue{}
		out.Body.Value = v
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "put-user-setting",
		Method:        http.MethodPut,
		Path:          "/api/v1/settings/user/{key}",
		Summary:       "Set a per-user UI preference",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *userSettingPutInput) (*struct{}, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("user settings service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		if err := svc.Set(ctx, id.Principal.OwnersPrincipal(), in.Key, in.Body.Value); err != nil {
			return nil, Translate(err)
		}
		return nil, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-user-setting",
		Method:        http.MethodDelete,
		Path:          "/api/v1/settings/user/{key}",
		Summary:       "Delete a per-user UI preference",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *userSettingPathInput) (*struct{}, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("user settings service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		if err := svc.Delete(ctx, id.Principal.OwnersPrincipal(), in.Key); err != nil {
			return nil, Translate(err)
		}
		return nil, nil
	})
}

package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/errs"
)

type meOutput struct {
	Body struct {
		Principal struct {
			Hub    string `json:"hub"`
			UserID string `json:"user_id"`
			Handle string `json:"handle,omitempty"`
		} `json:"principal"`
		Scopes   []string `json:"scopes"`
		Features struct {
			SharingEnabled bool `json:"sharing_enabled"`
		} `json:"features"`
	}
}

// registerMe wires GET /api/v1/me. sharingEnabled is the [ui].sharing_enabled
// config flag at boot; it surfaces under features.sharing_enabled so the SPA
// can hide share UI without the share data-plane changing shape.
func registerMe(api huma.API, sharingEnabled bool) {
	huma.Register(api, huma.Operation{
		OperationID: "me",
		Method:      http.MethodGet,
		Path:        "/api/v1/me",
		Summary:     "Return identity of the caller",
	}, func(ctx context.Context, _ *struct{}) (*meOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		out := &meOutput{}
		out.Body.Principal.Hub = id.Principal.Hub
		out.Body.Principal.UserID = id.Principal.UserID
		out.Body.Principal.Handle = id.Principal.Handle
		out.Body.Scopes = id.Scopes
		out.Body.Features.SharingEnabled = sharingEnabled
		return out, nil
	})
}

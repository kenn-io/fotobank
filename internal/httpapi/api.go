// Package httpapi wires the Fotobank HTTP API: a huma/v2 API mounted on a
// net/http.ServeMux, exposing operations under /api/v1/. It is the single
// entry point for serving HTTP requests in the fotobank daemon.
package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/version"
)

// Deps carries the collaborators that New needs to wire the HTTP API.
// Constructing Deps at the process edge (CLI or daemon main) keeps
// httpapi independent of how those collaborators are built.
type Deps struct {
	// IdentityProvider resolves the caller's Identity from each request.
	// Left nil for boot-only endpoints (such as /healthz) that do not
	// require authentication.
	IdentityProvider identity.Provider
	// OwnerService backs owner-scoped operations (for example /me in
	// Task 25). Left nil when only endpoints that do not touch owners
	// are registered.
	OwnerService *service.OwnerService
	// MediaService powers /api/v1/media list + detail + original. Nil
	// means those routes aren't registered; existing tests that don't
	// need them can pass Deps without a MediaService.
	MediaService *service.MediaService
	// ThumbService powers /api/v1/media/{id}/thumb. Nil means that route
	// isn't registered; tests and the OpenAPI spec dumper that don't
	// need thumb serving can pass Deps without a ThumbService.
	ThumbService *service.ThumbService
	// AlbumService backs /api/v1/albums CRUD and nested album_media
	// routes. Nil means those handlers answer 503 Service Unavailable so
	// the OpenAPI dumper can still emit the schema.
	AlbumService *service.AlbumService
	// ShareService backs /api/v1/shares CRUD plus the revoke/retry
	// transitions. Nil means those handlers answer 503 Service
	// Unavailable so the OpenAPI dumper can still emit the schema.
	ShareService *service.ShareService
	// SharedRead powers the grantee-side /api/v1/shared/* read surface.
	// Nil means those handlers answer 503 Service Unavailable so the
	// OpenAPI dumper can still emit the schema.
	SharedRead *service.SharedReadService
}

// New constructs the Fotobank HTTP handler: a net/http.ServeMux with a
// huma API layered on top. The returned handler serves every operation
// registered during setup; an error is returned if any registration or
// wiring step fails. When deps.IdentityProvider is non-nil the handler
// is wrapped with the identity + request-id + logging middleware.
func New(deps Deps) (http.Handler, error) {
	mux, _ := buildAPI(deps)
	if deps.IdentityProvider != nil {
		return WithMiddleware(deps.IdentityProvider)(mux), nil
	}
	return mux, nil
}

// buildAPI creates the shared mux + huma API and registers every
// operation exposed under /api/v1/. Both the runtime handler (New) and
// the spec dumper (OpenAPISpec) use it so that the served API and the
// documented API cannot drift apart. Registrations that depend on a
// collaborator (such as MediaService) are no-ops when the collaborator
// on deps is nil; OpenAPISpec can therefore pass Deps{} and still emit
// a spec for the routes that need no wiring.
func buildAPI(deps Deps) (*http.ServeMux, huma.API) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Fotobank", version.Short))
	api.OpenAPI().Info.Description = "Fotobank HTTP API"
	registerHealthz(api)
	registerMe(api)
	registerMedia(api, deps.MediaService)
	registerMediaOriginal(mux, deps.MediaService)
	registerMediaThumb(mux, deps.ThumbService)
	registerAlbums(api, deps.AlbumService)
	registerShares(api, deps.ShareService)
	registerShared(api, deps.SharedRead)
	return mux, api
}

type healthzOutput struct {
	Body struct {
		Status string `json:"status"`
	}
}

func registerHealthz(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "healthz",
		Method:      http.MethodGet,
		Path:        "/api/v1/healthz",
		Summary:     "Health check",
	}, func(_ context.Context, _ *struct{}) (*healthzOutput, error) {
		out := &healthzOutput{}
		out.Body.Status = "ok"
		return out, nil
	})
}

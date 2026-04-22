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
}

// New constructs the Fotobank HTTP handler: a net/http.ServeMux with a
// huma API layered on top. The returned handler serves every operation
// registered during setup; an error is returned if any registration or
// wiring step fails. When deps.IdentityProvider is non-nil the handler
// is wrapped with the identity + request-id + logging middleware.
func New(deps Deps) (http.Handler, error) {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("Fotobank", version.Short))
	api.OpenAPI().Info.Description = "Fotobank HTTP API"

	registerHealthz(api)
	registerMe(api)

	if deps.IdentityProvider != nil {
		return WithMiddleware(deps.IdentityProvider)(mux), nil
	}
	return mux, nil
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

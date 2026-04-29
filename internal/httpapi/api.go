// Package httpapi wires the Fotobank HTTP API: a huma/v2 API mounted on a
// net/http.ServeMux, exposing operations under /api/v1/. It is the single
// entry point for serving HTTP requests in the fotobank daemon.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/obs"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/service/usersettings"
	"github.com/wesm/fotobank/internal/share"
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
	// UserSettings backs /api/v1/settings/user/{key} get/put/delete. Nil
	// means those handlers answer 503 Service Unavailable so the OpenAPI
	// dumper can still emit the schema.
	UserSettings *usersettings.Service
	// EventBus is the SSE fanout for GET /api/v1/events. Nil means the
	// route is not registered (matching registerMediaOriginal /
	// registerMediaThumb), so the OpenAPI dumper and tests that don't
	// need streaming can pass Deps without one.
	EventBus *EventBus
	// PrincipalDisplay is the write side of the display-handle cache
	// (populated by WithPrincipalDisplayCache). Nil disables the
	// middleware — handles won't be refreshed from live traffic but the
	// rest of the API keeps working.
	PrincipalDisplay *share.PrincipalDisplayRepo
	// HiddenAuth is the hidden-privacy service used by WithHiddenUnlock to
	// validate cookies. Nil disables the middleware.
	HiddenAuth *hidden.Service
	// DevInsecureHiddenCookies, when true, configures WithHiddenUnlock to
	// use the dev cookie name (fotobank-hidden, no Secure flag) instead of
	// the production __Host-fotobank-hidden cookie.
	DevInsecureHiddenCookies bool
	// Logger is the base slog.Logger used by the request middleware to
	// build per-request loggers. nil falls back to slog.Default().
	Logger *slog.Logger
	// Metrics is the obs.Metrics registry used to record per-request
	// counters and histograms. nil disables metric recording but the
	// rest of the middleware still runs.
	Metrics *obs.Metrics
	// RequestIDHeader is the inbound header name to read for an
	// upstream-supplied request ID. Empty means generate a fresh UUID
	// per request. Wired from cfg.Identity.Header.RequestIDHeader so
	// the middleware honours the configured proxy header in header
	// mode (and ignores any request-supplied value in stub mode).
	RequestIDHeader string
}

// New constructs the Fotobank HTTP handler. The full middleware chain is:
//
//	metrics → recovery → identity → hidden-unlock → display-cache → mux
//
// Layers are only inserted when the relevant Deps fields are non-nil.
// hidden-unlock wraps display-cache so the resolved identity is already in
// context when the cookie is validated.
func New(deps Deps) (http.Handler, error) {
	mux, _ := buildAPI(deps)
	var handler http.Handler = mux
	if deps.PrincipalDisplay != nil {
		dispLogger := deps.Logger
		if dispLogger == nil {
			dispLogger = slog.Default()
		}
		handler = WithPrincipalDisplayCache(deps.PrincipalDisplay, dispLogger)(handler)
	}
	// hidden-unlock wraps display-cache (so identity is already attached and
	// display-cache can read the post-cookie context). Execution order is
	// identity → hidden-unlock → display-cache → mux.
	cookieCfg := hidden.CookieConfigFor(deps.DevInsecureHiddenCookies)
	handler = WithHiddenUnlock(handler, deps.HiddenAuth, cookieCfg, time.Now)
	if deps.IdentityProvider != nil {
		handler = WithMiddleware(WithMiddlewareDeps{
			Provider:        deps.IdentityProvider,
			Logger:          deps.Logger,
			Metrics:         deps.Metrics,
			RequestIDHeader: deps.RequestIDHeader,
		})(handler)
	}
	return handler, nil
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
	cfg := huma.DefaultConfig("Fotobank", version.Short)
	// Huma's defaults register the OpenAPI spec, schemas, and docs UI at
	// the document root (/openapi.{json,yaml}, /schemas, /docs). The
	// outer mux in cmd/fotobank/server mounts this handler under /api/
	// only, so anything at the root is routed to the SPA handler instead
	// — which would swallow these paths and serve HTML. Move all three
	// under /api/ so the doc surface lives alongside the JSON routes
	// (huma appends .json/.yaml to OpenAPIPath automatically, so the
	// runtime URLs become /api/openapi.json, /api/openapi.yaml,
	// /api/docs, /api/schemas/{name}).
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "/api/docs"
	cfg.SchemasPath = "/api/schemas"
	api := humago.New(mux, cfg)
	api.OpenAPI().Info.Description = "Fotobank HTTP API"
	registerHealthz(api)
	registerMe(api)
	registerMedia(api, deps.MediaService)
	registerMediaOriginal(mux, deps.MediaService)
	registerMediaThumb(mux, deps.ThumbService)
	registerAlbums(api, deps.AlbumService)
	registerShares(api, deps.ShareService, deps.PrincipalDisplay)
	registerShared(api, deps.SharedRead)
	registerSharedBytes(mux, deps.SharedRead)
	registerUserSettings(api, deps.UserSettings)
	registerEvents(mux, deps.EventBus)
	cookieCfg := hidden.CookieConfigFor(deps.DevInsecureHiddenCookies)
	registerHiddenAuth(api, deps.HiddenAuth, cookieCfg)
	return mux, api
}

// registerEvents wires GET /api/v1/events onto mux as a raw streaming
// route (SSE is not a JSON route, so it is not mounted via huma). bus
// may be nil; in that case the route is not registered and clients
// receive whatever default the surrounding mux returns. The handler
// reads the caller's Identity from the request context, which means
// the route must sit behind the same middleware chain that powers the
// huma routes — buildAPI is called before httpapi.New wraps the mux
// with WithMiddleware, so this requirement is satisfied automatically.
func registerEvents(mux *http.ServeMux, bus *EventBus) {
	if bus == nil {
		return
	}
	mux.Handle("GET /api/v1/events", WrapMuxHandler(eventsHandler(bus)))
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

package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/web"
)

// withBoth simulates a built dist: both index.html (the real SPA shell)
// and stub.html (the committed placeholder) are present.
func withBoth() fstest.MapFS {
	return fstest.MapFS{
		"stub.html":  &fstest.MapFile{Data: []byte("<html>STUB</html>")},
		"index.html": &fstest.MapFile{Data: []byte("<html>SPA</html>")},
	}
}

// withStubOnly simulates a fresh checkout before `make frontend`: only
// stub.html is present in the embed.
func withStubOnly() fstest.MapFS {
	return fstest.MapFS{
		"stub.html": &fstest.MapFile{Data: []byte("<html>STUB</html>")},
	}
}

func TestHandlerForServesIndexAt200WhenBuilt(t *testing.T) {
	// Regression: the previous implementation rewrote r.URL.Path to
	// "/index.html" and dispatched to http.FileServer, which then sent
	// a 301 to "/" via its directory-index canonicalization. Verify
	// the shell is served directly with HTTP 200.
	r := require.New(t)
	h := web.HandlerFor(withBoth())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	r.Equal(http.StatusOK, rr.Code)
	r.Contains(rr.Body.String(), "SPA")
	r.NotContains(rr.Body.String(), "STUB")
}

func TestHandlerForServesStubWhenIndexMissing(t *testing.T) {
	// Without index.html, pickShell falls back to stub.html so the
	// CI-before-`make frontend` checkout still serves something.
	r := require.New(t)
	h := web.HandlerFor(withStubOnly())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	r.Equal(http.StatusOK, rr.Code)
	r.Contains(rr.Body.String(), "STUB")
}

func TestHandlerForSPAFallbackServesIndex(t *testing.T) {
	// /library is a client-side route; with index.html present it must
	// fall back to the SPA shell at HTTP 200, not a 301.
	r := require.New(t)
	h := web.HandlerFor(withBoth())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/library", nil))
	r.Equal(http.StatusOK, rr.Code)
	r.Contains(rr.Body.String(), "SPA")
}

func TestHandlerForAssetMissingReturns404(t *testing.T) {
	// Asset 404s must keep working with the fstest.MapFS seam so the
	// /assets/ branch isn't accidentally captured by the shell fallback.
	r := require.New(t)
	h := web.HandlerFor(withBoth())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	r.Equal(http.StatusNotFound, rr.Code)
}

func TestHandlerForRejectsTraversalPath(t *testing.T) {
	// `..` segments still 404 cleanly via fs.ValidPath; the new
	// dispatch path must not regress this.
	r := require.New(t)
	h := web.HandlerFor(withBoth())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Path = "/../etc/passwd"
	h.ServeHTTP(rr, req)
	r.Equal(http.StatusNotFound, rr.Code)
}

func TestHandlerForRejectsAssetDirectoryListing(t *testing.T) {
	// After the trailing-slash strip both `/assets/` and `/assets`
	// resolve to the directory "assets", which exists as soon as the
	// SPA is built. http.FileServer would happily serve a directory
	// listing for those paths — exposing every hashed bundle name.
	// The handler must stat asset paths and 404 directories.
	r := require.New(t)
	mapfs := fstest.MapFS{
		"stub.html":             &fstest.MapFile{Data: []byte("<html>STUB</html>")},
		"index.html":            &fstest.MapFile{Data: []byte("<html>SPA</html>")},
		"assets/main-abc123.js": &fstest.MapFile{Data: []byte("console.log('hi')")},
	}
	h := web.HandlerFor(mapfs)
	for _, p := range []string{"/assets/", "/assets"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		r.Equal(http.StatusNotFound, rr.Code, "path: %s", p)
	}
	// Sanity: real asset files still serve.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/main-abc123.js", nil))
	r.Equal(http.StatusOK, rr.Code)
	r.Contains(rr.Body.String(), "console.log")
}

func TestHandlerForServesShellForTrailingSlashRoute(t *testing.T) {
	// /library/ is a valid SPA route — the trailing slash is browser
	// canonicalization, not a path-traversal attempt. fs.ValidPath
	// rejects trailing slashes, so the handler must strip the slash
	// before validation; otherwise a hard reload of /library/ would
	// 404 instead of falling through to the SPA shell.
	r := require.New(t)
	h := web.HandlerFor(withBoth())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/library/", nil))
	r.Equal(http.StatusOK, rr.Code)
	r.Contains(rr.Body.String(), "SPA")
}

package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/web"
)

func TestHandlerServesRoot(t *testing.T) {
	r := require.New(t)
	h := web.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	r.Equal(http.StatusOK, rr.Code)
	r.Contains(rr.Body.String(), "<html") // index.html OR stub.html
}

func TestHandlerSPAFallback(t *testing.T) {
	// Client-side routes (anything that isn't a real file in dist) must
	// fall back to the SPA shell so a hard reload of /library or /sessions
	// still loads the app.
	r := require.New(t)
	h := web.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/library", nil))
	r.Equal(http.StatusOK, rr.Code)
	r.Contains(rr.Body.String(), "<html")
}

func TestHandlerStaticAssetMissingReturns404(t *testing.T) {
	// A missing .js or .css under /assets is a build error, not a SPA
	// route. Verify the fallback only applies to non-asset paths so
	// build-pipeline issues stay visible instead of being masked by HTML.
	r := require.New(t)
	h := web.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	r.Equal(http.StatusNotFound, rr.Code)
}

func TestHandlerRejectsTraversalPath(t *testing.T) {
	// `..` segments would otherwise be cleaned by net/http and silently
	// fall through to the SPA shell, returning 200 with HTML for a path
	// the caller never expected to resolve. fs.ValidPath rejects them
	// up front so they 404.
	r := require.New(t)
	h := web.Handler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Path = "/../etc/passwd"
	h.ServeHTTP(rr, req)
	r.Equal(http.StatusNotFound, rr.Code)
}

func TestEmbedExcludesDotfiles(t *testing.T) {
	// Build outputs, not git plumbing, are what the embed should serve.
	// Hidden placeholders (.gitignore, .gitkeep) live in the dist tree
	// for git's benefit and should not be reachable over HTTP. The
	// default //go:embed directive (no `all:` prefix) excludes these,
	// so the handler should either 404 or return the SPA shell — never
	// the file's actual content.
	//
	// Sentinels: dist/.gitignore literally contains "!stub.html" (a
	// negation rule that whitelists the committed placeholder) and
	// "Built SPA goes here". Neither string appears in stub.html or in
	// the built index.html, so finding either one in the response body
	// proves the embed leaked the .gitignore bytes.
	r := require.New(t)
	h := web.Handler()
	for _, p := range []string{"/.gitignore", "/.gitkeep"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		body := rr.Body.String()
		r.NotContains(body, "!stub.html", "dotfile leak: %s", p)
		r.NotContains(body, "Built SPA goes here", "dotfile leak: %s", p)
	}
}

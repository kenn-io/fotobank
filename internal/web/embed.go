// Package web embeds the built SPA and serves it. The same handler also
// implements the SPA fallback: any GET that does not match a real file in
// dist (and is not a static asset under /assets/) falls back to index.html
// so client-side routes resolve on hard reload. When the frontend hasn't
// been built yet (e.g. CI before `make frontend`) the handler falls back
// to a minimal stub.html that explains how to build the SPA.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded SPA. Construct
// once at startup; the returned handler caches the shell choice. It must
// be mounted AFTER /api/v1/* routes; the SPA fallback is path-agnostic
// and would otherwise swallow API requests.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: failed to scope embed.FS to dist: " + err.Error())
	}
	return HandlerFor(sub)
}

// HandlerFor returns the SPA handler scoped to the given filesystem.
// Exposed for tests so they can pass an in-memory fs.FS that includes
// index.html; the production embed only ever holds stub.html in the
// committed tree (the built SPA is gitignored). Production code uses
// Handler().
func HandlerFor(sub fs.FS) http.Handler {
	fsHandler := http.FileServer(http.FS(sub))
	shell := pickShell(sub) // "index.html" or "stub.html"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		// Reject malformed paths (`..` segments, absolute paths) up
		// front so they 404 cleanly instead of getting cleaned to "/"
		// and silently rewritten to the SPA shell.
		if path != "" && !fs.ValidPath(path) {
			http.NotFound(w, r)
			return
		}
		// Static assets must 404 cleanly so build issues surface. Vite
		// emits all hashed bundle output under /assets/ (see
		// frontend/vite.config.ts); expand this list if the build tool
		// changes. FileServer is the right tool here because we want
		// 404s for missing assets, not redirects.
		if strings.HasPrefix(path, "assets/") {
			fsHandler.ServeHTTP(w, r)
			return
		}
		// Root or unknown SPA route: serve the shell directly via
		// ServeFileFS. Going through FileServer with a rewritten path
		// would trigger its /index.html → / canonicalization redirect
		// (HTTP 301) instead of returning the file bytes.
		if path == "" || !exists(sub, path) {
			http.ServeFileFS(w, r, sub, shell)
			return
		}
		fsHandler.ServeHTTP(w, r)
	})
}

// pickShell returns "index.html" if a built SPA is present, else
// "stub.html". The dist directory always contains at least stub.html
// (committed alongside .gitkeep / .gitignore) so the handler always has
// a fallback target.
func pickShell(sub fs.FS) string {
	if exists(sub, "index.html") {
		return "index.html"
	}
	return "stub.html"
}

// exists reports whether the given path resolves to a file in sub.
func exists(sub fs.FS, path string) bool {
	_, err := fs.Stat(sub, path)
	return err == nil
}

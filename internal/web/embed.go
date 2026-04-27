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
		// changes.
		if strings.HasPrefix(path, "assets/") {
			fsHandler.ServeHTTP(w, r)
			return
		}
		// Root or unknown SPA route: rewrite to the shell so the
		// FileServer serves it directly (not a directory listing) and
		// client-side routing handles the URL.
		if path == "" || !exists(sub, path) {
			r.URL.Path = "/" + shell
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

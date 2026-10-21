// Package ui serves the shared Lit component library: the bundle that
// build.mjs writes to dist/. dist/ is built, not committed (only dist/.gitkeep
// is), so run "task build-ui" before building an app that mounts Handler.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// Prefix is the URL path every app mounts Handler at.
const Prefix = "/ui/"

//go:embed all:dist
var distFS embed.FS

// Handler serves the built bundle under Prefix.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("ui: embedded dist directory is missing: " + err.Error())
	}
	return newHandler(sub)
}

// newHandler serves fsys under Prefix. Chunks have a content hash in their
// name and never change, so they are cached for good; everything else
// (dp-core.js, tokens.css) is revalidated on every load.
func newHandler(fsys fs.FS) http.Handler {
	files := http.StripPrefix(Prefix, http.FileServerFS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, Prefix+"chunks/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

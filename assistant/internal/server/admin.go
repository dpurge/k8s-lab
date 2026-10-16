package server

import (
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// The admin interface is served from the binary: no build step, no bundler.
// Only the files the page needs are embedded, listed by pattern so that tests,
// the test tooling and its node_modules never end up in the image.
//
//go:embed static/index.html static/*.js static/*.css static/components/*/component.js static/components/*/component.css
var staticFiles embed.FS

// adminPolicy lets the page load only its own scripts and styles and talk only
// to its own server: nothing inline, nothing from elsewhere, no framing.
const adminPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// mountAdmin serves the admin interface under /admin/.
func (d Deps) mountAdmin(r chi.Router) {
	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("embedded admin files: " + err.Error()) // the embed pattern is fixed at build time
	}
	r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/admin/")
		w.WriteHeader(http.StatusMovedPermanently)
	})
	r.Get("/admin/*", serveAsset(assets))
}

// serveAsset serves one embedded file. A path that is empty means the index; a
// path that is not a plain relative path, or names a directory or a file that
// is not embedded, is a 404 in the API's error shape.
func serveAsset(assets fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "*")
		if name == "" {
			name = "index.html"
		}
		if !fs.ValidPath(name) || strings.ContainsAny(name, `\`) || path.Clean(name) != name {
			writeError(w, http.StatusNotFound, codeNotFound, "no such file")
			return
		}
		f, err := assets.Open(name)
		if err != nil {
			writeError(w, http.StatusNotFound, codeNotFound, "no such file")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		seeker, ok := f.(io.ReadSeeker)
		if err != nil || info.IsDir() || !ok {
			writeError(w, http.StatusNotFound, codeNotFound, "no such file")
			return
		}

		h := w.Header()
		h.Set("Content-Type", contentType(name))
		h.Set("Content-Security-Policy", adminPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// The files change with each release and are small, so they are always
		// revalidated instead of cached for a while.
		h.Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, time.Time{}, seeker)
	}
}

// contentType does not rely on the system's MIME table, which a minimal image
// does not have.
func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

package server

import (
	"embed"
	"encoding/json"
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
// to its own server: nothing inline, nothing from elsewhere, no framing. Images,
// video and audio may come from this server, from data: URLs and from the
// configured media hosts over https (a chat interface drawn by A2UI can show
// them); those hosts are the only thing the policy takes from configuration,
// and config.ParseMediaHosts has already limited each to a bare host name.
func adminPolicy(mediaHosts []string) string {
	var hosts strings.Builder
	for _, host := range mediaHosts {
		hosts.WriteString(" https://" + host)
	}
	return "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:" + hosts.String() +
		"; media-src 'self' data:" + hosts.String() + "; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

// mountAdmin serves the admin interface: the page at / and its files under
// /static/, as phraseforge does. The old /admin and /admin/ addresses redirect
// to /.
func (d Deps) mountAdmin(r chi.Router) {
	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("embedded admin files: " + err.Error()) // the embed pattern is fixed at build time
	}
	toRoot := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusMovedPermanently)
	}
	r.Get("/admin", toRoot)
	r.Get("/admin/", toRoot)
	policy := adminPolicy(d.A2UIMediaHosts)
	r.Get("/", servePage(assets, policy))
	r.Get("/static/*", serveAsset(assets, policy))
	r.Get("/a2ui-config.json", d.serveA2UIConfig(policy))
}

// serveA2UIConfig tells the page which media hosts its policy allows, so the
// renderer can explain a blocked image instead of showing a broken one.
func (d Deps) serveA2UIConfig(policy string) http.HandlerFunc {
	hosts := d.A2UIMediaHosts
	if hosts == nil {
		hosts = []string{}
	}
	body, err := json.Marshal(map[string]any{"media_hosts": hosts})
	if err != nil {
		panic("a2ui config: " + err.Error()) // a list of strings always marshals
	}
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	}
}

// servePage serves the single page of the admin interface.
func servePage(assets fs.FS, policy string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { serveFile(w, r, assets, "index.html", policy) }
}

// serveAsset serves the file named by the rest of the /static/ path.
func serveAsset(assets fs.FS, policy string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { serveFile(w, r, assets, chi.URLParam(r, "*"), policy) }
}

// serveFile serves one embedded file. A name that is not a plain relative
// path, or names a directory or a file that is not embedded, is a 404 in the
// API's error shape.
func serveFile(w http.ResponseWriter, r *http.Request, assets fs.FS, name, policy string) {
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
	h.Set("Content-Security-Policy", policy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// The files change with each release and are small, so they are always
	// revalidated instead of cached for a while.
	h.Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, time.Time{}, seeker)
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

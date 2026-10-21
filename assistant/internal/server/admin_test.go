package server

import (
	"assistant/internal/metrics"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func adminGet(t *testing.T, h http.Handler, s *spec, route, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	s.check("GET", route, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
	return rec
}

func TestAdminPageIsServedWithAStrictPolicy(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	s := loadSpec(t)
	rec := adminGet(t, h, s, "/", "/")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "<title>Assistant</title>") {
		t.Errorf("not the admin page: %.200s", rec.Body)
	}
	policy := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy lacks %q: %s", want, policy)
		}
	}
	if strings.Contains(policy, "unsafe") || strings.Contains(policy, "*") {
		t.Errorf("policy must not weaken itself: %s", policy)
	}
	for name, want := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-cache"} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestOldAdminAddressesRedirectToTheRoot(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	s := loadSpec(t)
	for _, p := range []string{"/admin", "/admin/"} {
		rec := adminGet(t, h, s, p, p)
		if rec.Code != 301 || rec.Header().Get("Location") != "/" {
			t.Errorf("%s: status %d location %q", p, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestEveryFileThePageLoadsIsServedWithTheRightType(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	s := loadSpec(t)
	index := adminGet(t, h, s, "/", "/").Body.String()
	refs := regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`).FindAllStringSubmatch(index, -1)
	if len(refs) < 12 {
		t.Fatalf("found only %d references in the page", len(refs))
	}
	for _, m := range refs {
		rec := adminGet(t, h, s, "/static/{path}", m[1])
		want := map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8"}[filepath.Ext(m[1])]
		if rec.Code != 200 || rec.Header().Get("Content-Type") != want {
			t.Errorf("%s: status %d type %q, want %q", m[1], rec.Code, rec.Header().Get("Content-Type"), want)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s is empty", m[1])
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s has no policy", m[1])
		}
	}
}

func TestTestingAndToolingFilesAreNotServed(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	s := loadSpec(t)
	for _, p := range []string{
		"/static/components/as-task-list/component.test.js",
		"/static/components/testenv.js",
		"/static/components/package.json",
		"/static/components/package-lock.json",
		"/static/components/node_modules/jsdom/package.json",
		"/static/theme.test.js",
	} {
		rec := adminGet(t, h, s, "/static/{path}", p)
		if rec.Code != 404 {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
}

func TestPathTricksDoNotEscapeTheAssetTree(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	s := loadSpec(t)
	for _, p := range []string{
		"/static/../server.go", "/static/%2e%2e/server.go", "/static/..%2fserver.go", "/static/components/../../server.go",
		"/static//index.html", "/static/./index.html", "/static/components/", "/static/components", `/static/..\server.go`, "/static/%00", "/static/components/as-nav/",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", p, nil)
		h.ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Errorf("%s: served (%d) %.60q", p, rec.Code, rec.Body)
		}
		if rec.Code == 404 {
			s.check("GET", "/static/{path}", 404, rec.Header().Get("Content-Type"), rec.Body.Bytes())
		}
	}
}

func TestUnknownAssetIsA404InTheErrorShape(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	s := loadSpec(t)
	a := &api{t: t, h: h, spec: s}
	a.wantError(adminGet(t, h, s, "/static/{path}", "/static/nope.js"), 404, "not_found", "no such file")
}

func TestOnlyGETIsServed(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/", nil))
		if rec.Code != 405 {
			t.Errorf("%s: status %d", m, rec.Code)
		}
	}
}

// listEmbedded returns every embedded file path.
func listEmbedded(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(staticFiles, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, strings.TrimPrefix(p, "static/"))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestOnlyTheFilesThePageNeedsAreEmbedded(t *testing.T) {
	embedded := listEmbedded(t)
	for _, f := range embedded {
		switch {
		case strings.Contains(f, "node_modules"), strings.HasSuffix(f, ".test.js"), strings.HasSuffix(f, "testenv.js"), strings.HasSuffix(f, "package.json"), strings.HasSuffix(f, "package-lock.json"):
			t.Errorf("%s must not be in the image", f)
		}
	}
	// Everything embedded is referenced by the page or by a component that is.
	var corpus strings.Builder
	for _, f := range embedded {
		if strings.HasSuffix(f, ".js") || strings.HasSuffix(f, ".html") {
			data, err := staticFiles.ReadFile("static/" + f)
			if err != nil {
				t.Fatal(err)
			}
			corpus.Write(data)
		}
	}
	for _, f := range embedded {
		if f == "index.html" {
			continue
		}
		if !strings.Contains(corpus.String(), "/static/"+f) {
			t.Errorf("%s is embedded but nothing refers to it as /static/%s", f, f)
		}
	}
	if len(embedded) < 19 {
		t.Errorf("only %d files embedded: %v", len(embedded), embedded)
	}
}

func TestEveryComponentOnDiskIsEmbedded(t *testing.T) {
	// A new component directory whose files the embed pattern misses would
	// work in tests and be absent from the image.
	dirs, err := os.ReadDir("static/components")
	if err != nil {
		t.Fatal(err)
	}
	embedded := map[string]bool{}
	for _, f := range listEmbedded(t) {
		embedded[f] = true
	}
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "node_modules" {
			continue
		}
		for _, f := range []string{"component.js", "component.css"} {
			if !embedded["components/"+d.Name()+"/"+f] {
				t.Errorf("components/%s/%s is not embedded", d.Name(), f)
			}
		}
	}
}

// The admin interface must call only operations the API documents.
func TestTheSPACallsOnlyDocumentedAPIOperations(t *testing.T) {
	s := loadSpec(t)
	documented := s.operations()
	ref := regexp.MustCompile("[\"'`](/api/v1/[A-Za-z0-9_/-]*)")
	found := 0
	for _, f := range listEmbedded(t) {
		if !strings.HasSuffix(f, ".js") {
			continue
		}
		data, _ := staticFiles.ReadFile("static/" + f)
		for _, m := range ref.FindAllStringSubmatch(string(data), -1) {
			found++
			call := m[1]
			ok := false
			for op := range documented {
				_, p, _ := strings.Cut(op, " ")
				if !strings.HasPrefix(p, "/api/v1/") {
					continue
				}
				// /api/v1/tasks/ followed by an id matches /api/v1/tasks/{id}.
				if call == p || strings.HasSuffix(call, "/") && strings.HasPrefix(p, call) && strings.Count(p, "/") == strings.Count(call, "/") {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s calls %s, which openapi.yaml does not document", f, call)
			}
		}
	}
	if found < 3 {
		t.Errorf("found %d API calls in the scripts; the check is not looking at the right thing", found)
	}
}

// base is the policy with no media hosts configured.
const basePolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; media-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func TestThePolicyIsFixedUnlessMediaHostsAreConfigured(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	if got := do(h, "GET", "/").Header().Get("Content-Security-Policy"); got != basePolicy {
		t.Errorf("policy = %q\nwant     %q", got, basePolicy)
	}
}

func TestConfiguredMediaHostsAreTheOnlyChangeToThePolicy(t *testing.T) {
	h := NewRouter(Deps{DB: fakePinger{}, Metrics: metrics.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), A2UIMediaHosts: []string{"images.example.com", "media.example.com:8443"}})
	want := "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data: https://images.example.com https://media.example.com:8443; media-src 'self' data: https://images.example.com https://media.example.com:8443; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	for _, path := range []string{"/", "/static/app.js", "/a2ui-config.json"} {
		if got := do(h, "GET", path).Header().Get("Content-Security-Policy"); got != want {
			t.Errorf("%s: policy = %q\nwant %q", path, got, want)
		}
	}
}

func TestTheA2UIConfigListsTheMediaHosts(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	rec := do(h, "GET", "/a2ui-config.json")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || strings.TrimSpace(rec.Body.String()) != `{"media_hosts":[]}` {
		t.Errorf("status %d type %q body %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	h = NewRouter(Deps{DB: fakePinger{}, Metrics: metrics.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), A2UIMediaHosts: []string{"images.example.com"}})
	if body := strings.TrimSpace(do(h, "GET", "/a2ui-config.json").Body.String()); body != `{"media_hosts":["images.example.com"]}` {
		t.Errorf("body %q", body)
	}
}

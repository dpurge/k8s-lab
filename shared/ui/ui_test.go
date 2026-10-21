package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHandlerServesBundleUnderPrefix(t *testing.T) {
	h := newHandler(fstest.MapFS{
		"dp-core.js":               {Data: []byte("core")},
		"tokens.css":               {Data: []byte(":root{}")},
		"chunks/dp-chat-ABC123.js": {Data: []byte("chunk")},
	})

	tests := []struct {
		path      string
		status    int
		body      string
		cacheCtrl string
	}{
		{"/ui/dp-core.js", http.StatusOK, "core", "no-cache"},
		{"/ui/tokens.css", http.StatusOK, ":root{}", "no-cache"},
		{"/ui/chunks/dp-chat-ABC123.js", http.StatusOK, "chunk", "public, max-age=31536000, immutable"},
		{"/ui/missing.js", http.StatusNotFound, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(t, h, tc.path)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if tc.body != "" && rec.Body.String() != tc.body {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.body)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.cacheCtrl {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.cacheCtrl)
			}
		})
	}
}

func TestHandlerWorksBeforeTheBundleIsBuilt(t *testing.T) {
	// A clean checkout has only dist/.gitkeep: Handler must still construct,
	// and a request for a file that is not built is a plain 404.
	rec := get(t, Handler(), "/ui/dp-core.js")
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 404 (unbuilt) or 200 (built)", rec.Code)
	}
}

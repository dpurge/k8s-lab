package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchURLSuccessfulHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<p>hello</p>"))
	}))
	defer srv.Close()

	body, contentType, err := fetchURL(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchURL() error = %v, want nil", err)
	}
	if string(body) != "<p>hello</p>" {
		t.Errorf("fetchURL() body = %q, want %q", body, "<p>hello</p>")
	}
	if contentType != "text/html" {
		t.Errorf("fetchURL() contentType = %q, want %q", contentType, "text/html")
	}
}

func TestFetchURLSuccessfulPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	body, contentType, err := fetchURL(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchURL() error = %v, want nil", err)
	}
	if string(body) != "hello world" {
		t.Errorf("fetchURL() body = %q, want %q", body, "hello world")
	}
	if contentType != "text/plain" {
		t.Errorf("fetchURL() contentType = %q, want %q", contentType, "text/plain")
	}
}

func TestFetchURLNonSuccessStatusOmitsBody(t *testing.T) {
	const secretBody = "internal-service-error-detail-should-not-leak"
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(status)
			w.Write([]byte(secretBody))
		}))

		_, _, err := fetchURL(context.Background(), srv.URL)
		srv.Close()

		if err == nil {
			t.Fatalf("fetchURL() status %d: error = nil, want error", status)
		}
		if strings.Contains(err.Error(), secretBody) {
			t.Errorf("fetchURL() status %d: error = %q, must not contain response body", status, err.Error())
		}
	}
}

func TestFetchURLDisallowedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"a":1}`))
	}))
	defer srv.Close()

	_, _, err := fetchURL(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("fetchURL() error = nil, want error for disallowed content-type")
	}
}

func TestFetchURLMissingContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Explicitly set to empty so the net/http server doesn't sniff and
		// fill in a Content-Type header on our behalf: this simulates a
		// response that genuinely has no Content-Type.
		w.Header().Set("Content-Type", "")
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	_, _, err := fetchURL(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("fetchURL() error = nil, want error for missing content-type")
	}
}

func TestFetchURLOversizedBody(t *testing.T) {
	oversized := make([]byte, MaxSourceBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write(oversized)
	}))
	defer srv.Close()

	_, _, err := fetchURL(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("fetchURL() error = nil, want error for oversized body")
	}
}

func TestFetchURLBadScheme(t *testing.T) {
	_, _, err := fetchURL(context.Background(), "ftp://example.com/file.txt")
	if err == nil {
		t.Fatal("fetchURL() error = nil, want error for non-http(s) scheme")
	}
}

func TestFetchURLUnparsableOrEmptyHost(t *testing.T) {
	cases := []string{
		"http://",
		"://not-a-url",
		"",
	}
	for _, raw := range cases {
		_, _, err := fetchURL(context.Background(), raw)
		if err == nil {
			t.Errorf("fetchURL(%q) error = nil, want error", raw)
		}
	}
}

func TestValidateTextValidExtensions(t *testing.T) {
	cases := []string{"notes.txt", "notes.md", "notes.markdown", "NOTES.TXT"}
	for _, filename := range cases {
		if err := validateText(filename, "some real content"); err != nil {
			t.Errorf("validateText(%q, ...) error = %v, want nil", filename, err)
		}
	}
}

func TestValidateTextDisallowedExtension(t *testing.T) {
	cases := []string{"notes.pdf", "notes.docx", "notes"}
	for _, filename := range cases {
		if err := validateText(filename, "some real content"); err == nil {
			t.Errorf("validateText(%q, ...) error = nil, want error", filename)
		}
	}
}

func TestValidateTextEmptyOrWhitespaceContent(t *testing.T) {
	cases := []string{"", "   ", "\n\t  \n"}
	for _, content := range cases {
		if err := validateText("notes.txt", content); err == nil {
			t.Errorf("validateText(notes.txt, %q) error = nil, want error", content)
		}
	}
}

func TestValidateTextInvalidUTF8(t *testing.T) {
	invalid := "valid text \xff\xfe more text"
	if err := validateText("notes.txt", invalid); err == nil {
		t.Error("validateText() error = nil, want error for invalid UTF-8")
	}
}

func TestValidateTextOverSizeCap(t *testing.T) {
	oversized := strings.Repeat("a", MaxSourceBytes+1)
	if err := validateText("notes.txt", oversized); err == nil {
		t.Error("validateText() error = nil, want error for content over size cap")
	}
}

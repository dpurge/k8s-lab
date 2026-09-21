package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxSourceBytes bounds both a fetched URL's body and an uploaded text
	// file's content.
	MaxSourceBytes = 2 << 20
	// fetchTimeout bounds a single URL fetch, matching qdrant.New's client
	// timeout.
	fetchTimeout = 30 * time.Second
)

// ErrEmptyContent is returned when an uploaded text source's raw content
// string is empty or whitespace-only. validateText checks this on the raw
// string before it ever reaches chunking. Distinct from ErrNoContent
// (chunk.go), which fires later — inside Chunk itself, after
// paragraph-splitting has run, for input that is non-empty as a string but
// still has nothing usable in it (e.g. only punctuation with no
// paragraphs), or for a "url" source's fetched body, which never passes
// through this check at all.
var ErrEmptyContent = errors.New("ingest: content is required")

// allowedContentTypes is the set of source content types the pipeline will
// accept from a fetched URL. Anything else is rejected before the body is
// ever chunked or sent to an LLM.
var allowedContentTypes = map[string]bool{
	"text/html":             true,
	"text/plain":            true,
	"text/markdown":         true,
	"application/xhtml+xml": true,
}

// allowedTextExtensions is the set of file extensions accepted for an
// uploaded text source.
var allowedTextExtensions = map[string]bool{
	".txt":      true,
	".md":       true,
	".markdown": true,
}

// validateURL rejects an empty string, an unparsable URL, an unsupported
// scheme, or a missing host. Used both synchronously by StartURL (so a
// malformed URL fails the request with a 400 rather than starting a job
// that immediately fails) and by fetchURL itself, which has no other
// caller to trust that the check already ran.
func validateURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("url %q has unsupported scheme %q, want http or https", rawURL, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q has no host", rawURL)
	}
	return nil
}

// fetchURL retrieves raw over HTTP(S) and returns its body and content type.
// The scheme is restricted to http/https, the response is capped at
// MaxSourceBytes, and the content type must be on the allowlist. Error
// messages deliberately never include response body content: a fetched
// error page from a cluster-internal service must not be exfiltrated via an
// error string.
func fetchURL(ctx context.Context, raw string) ([]byte, string, error) {
	if err := validateURL(raw); err != nil {
		return nil, "", fmt.Errorf("ingest: %w", err)
	}

	client := &http.Client{Timeout: fetchTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", fmt.Errorf("ingest: build request for %s: %w", raw, err)
	}
	// Go's default "Go-http-client/1.1" User-Agent is a well-known bot
	// signature that many real-world sites (e.g. Wikipedia) reject outright
	// with a 403, even though a plain, descriptive one is accepted —
	// confirmed directly against en.wikipedia.org before this fix.
	req.Header.Set("User-Agent", "knowledge-ingest/1.0 (local dev tool)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("ingest: fetch %s: %w", raw, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("ingest: fetch %s: unexpected status %d", raw, resp.StatusCode)
	}

	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !allowedContentTypes[contentType] {
		// The fetched server controls this header, and it flows into
		// jobs.error (persisted in Postgres) and back out through
		// /api/v1/jobs/current, so it must be bounded before it's reflected.
		got := resp.Header.Get("Content-Type")
		const maxReflectedLen = 64
		if got == "" {
			got = "(missing)"
		} else if len(got) > maxReflectedLen {
			got = got[:maxReflectedLen] + "...(truncated)"
		}
		return nil, "", fmt.Errorf("ingest: fetch %s: unsupported content-type %q", raw, got)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSourceBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("ingest: fetch %s: read body: %w", raw, err)
	}
	if len(body) > MaxSourceBytes {
		return nil, "", fmt.Errorf("ingest: fetch %s: body exceeds %d byte limit", raw, MaxSourceBytes)
	}

	return body, contentType, nil
}

// validateText checks an uploaded text source's filename and content before
// it enters the chunking pipeline: extension allowlist, non-empty content,
// valid UTF-8, and the same size cap applied to fetched URLs.
func validateText(filename, content string) error {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return fmt.Errorf("ingest: filename %q has no extension, want one of .txt, .md, .markdown", filename)
	}
	if !allowedTextExtensions[ext] {
		return fmt.Errorf("ingest: filename %q has unsupported extension %q, want one of .txt, .md, .markdown", filename, ext)
	}
	if strings.TrimSpace(content) == "" {
		return ErrEmptyContent
	}
	if !utf8.ValidString(content) {
		return fmt.Errorf("ingest: content is not valid UTF-8")
	}
	if len(content) > MaxSourceBytes {
		return fmt.Errorf("ingest: content of %d bytes exceeds %d byte limit", len(content), MaxSourceBytes)
	}
	return nil
}

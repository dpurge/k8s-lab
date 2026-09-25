package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"phraseforge/internal/catalog"
	"phraseforge/internal/i18n"
	"phraseforge/internal/ingest"
	"phraseforge/internal/jobs"
)

// ingestSourceURL/ingestSourceFile/ingestSourceText are apiIngestRequest's
// allowed "source" values.
const (
	ingestSourceURL  = "url"
	ingestSourceFile = "file"
	ingestSourceText = "text"
)

const (
	// ingestMaxContentBytes caps the resolved raw content the ingest endpoint
	// will enqueue, checked after any HTML-stripping. This is deliberately
	// far below the 200 KB the feature's spec first named: process_text/
	// process_dialog/title's configured NumCtx is 8192 tokens (see
	// config.go's Title/ProcessText/ProcessDialog defaults), and Ollama
	// silently truncates a prompt that overflows its context window rather
	// than erroring — so a cap picked without regard to NumCtx would let a
	// large ingest silently produce a partial cleaned result instead of a
	// clear failure. At a conservative ~4 characters/token for prose, 8192
	// tokens is roughly 32 KB of raw text; 24 KB leaves headroom for the
	// system prompt template and the "Source language/Target language/
	// Content type" preamble ai.Service.Generate adds around the content
	// itself, all of which share the same context window. If NumCtx is ever
	// raised for these purposes, this constant must be revisited alongside
	// it — the two are directly linked, not independent knobs, and changing
	// one without the other silently reintroduces the truncation risk this
	// comment exists to prevent.
	ingestMaxContentBytes = 24 * 1024

	// ingestRequestBodyHeadroom is added to ingestMaxContentBytes to size
	// the http.MaxBytesReader wrapping the ingest endpoint's raw request
	// body — enough slack for the request's other JSON fields and for the
	// content field's own JSON-string escaping overhead, without letting an
	// oversized/malicious body be fully buffered into memory before the
	// content-length check below ever runs.
	ingestRequestBodyHeadroom = 16 * 1024

	// ingestFetchTimeout bounds a single URL fetch.
	ingestFetchTimeout = 15 * time.Second

	// ingestFetchMaxBytes caps how much of a fetched URL's response body is
	// read. An oversized response is rejected outright (see fetchIngestURL)
	// rather than silently truncated: a truncated HTML document can leave an
	// unclosed <script>/<style> tag whose content then survives stripHTML,
	// which would be worse than a clear 400.
	ingestFetchMaxBytes = 5 << 20 // 5 MB

	// ingestMaxRedirects bounds how many redirects fetchIngestURL follows
	// before giving up — far below net/http's default Client behavior (up to
	// 10), since an ingest URL fetch has no legitimate reason to hop through
	// many redirects, and every extra hop is one more request whose
	// destination ingestDialer's Control hook (below) has to re-validate.
	ingestMaxRedirects = 3
)

// apiIngestRequest is the JSON body shape for both
// POST /api/v1/texts/ingest and POST /api/v1/dialogs/ingest.
type apiIngestRequest struct {
	Source   string `json:"source"` // "url" | "file" | "text"
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Content  string `json:"content"`
	Language string `json:"language"`
	Script   string `json:"script"`
}

// ingestJobPayload mirrors ingest.processPayload's JSON shape field-for-field
// (that type is unexported, so this is a deliberate duplicate — a job
// payload is a wire contract between independently-versioned packages, not a
// shared Go type).
type ingestJobPayload struct {
	Content  string `json:"content"`
	Source   string `json:"source"`
	Language string `json:"language"`
	Script   string `json:"script"`
	UserID   int64  `json:"user_id"`
}

// apiIngestText handles POST /api/v1/texts/ingest.
func (s *Server) apiIngestText(w http.ResponseWriter, r *http.Request) {
	s.apiIngest(w, r, ingest.KindProcessText, "texts.err_no_edit_language")
}

// apiIngestDialog handles POST /api/v1/dialogs/ingest.
func (s *Server) apiIngestDialog(w http.ResponseWriter, r *http.Request) {
	s.apiIngest(w, r, ingest.KindProcessDialog, "dialogs.err_no_edit_language")
}

// apiIngest is apiIngestText/apiIngestDialog's shared body: validate the
// request, authorize via the same CanEdit check the "New" form's create
// endpoint already uses, resolve the raw content (pasted, uploaded, or
// fetched from a URL), and enqueue the process_text/process_dialog job that
// does the rest of the pipeline asynchronously.
func (s *Server) apiIngest(w http.ResponseWriter, r *http.Request, kind, forbiddenKey string) {
	// Bound the request body before anything else touches it: without this,
	// an oversized (or malicious) "content" field is fully decoded and
	// buffered into memory before the ingestMaxContentBytes check below ever
	// runs. The cap is content's own limit plus a small headroom for the
	// request's other fields and JSON-string escaping overhead (see
	// ingestRequestBodyHeadroom's doc comment) — not simply
	// ingestMaxContentBytes itself.
	r.Body = http.MaxBytesReader(w, r.Body, ingestMaxContentBytes+ingestRequestBodyHeadroom)

	u := currentUser(r)
	var req apiIngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.Language) == "" || strings.TrimSpace(req.Script) == "" {
		writeErr(w, http.StatusBadRequest, "validation_error", "language and script are required")
		return
	}

	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, req.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, forbiddenKey))
		return
	}

	// Reject an unknown language/script before doing any real work (a URL
	// fetch, or the process_text/title LLM calls the enqueued job will make)
	// — without this, an invalid value only surfaces later as a Text/Dialog
	// FK constraint failure, after those calls already ran.
	if _, ok, err := catalog.GetLanguageIfExists(r.Context(), s.db, req.Language); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	} else if !ok {
		writeErr(w, http.StatusBadRequest, "validation_error", "language does not exist")
		return
	}
	if _, ok, err := catalog.GetScriptIfExists(r.Context(), s.db, req.Script); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	} else if !ok {
		writeErr(w, http.StatusBadRequest, "validation_error", "script does not exist")
		return
	}

	content, source, err := resolveIngestContent(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if len(content) > ingestMaxContentBytes {
		writeErr(w, http.StatusBadRequest, "validation_error", fmt.Sprintf("content of %d bytes exceeds %d byte limit", len(content), ingestMaxContentBytes))
		return
	}

	payload, err := json.Marshal(ingestJobPayload{
		Content: content, Source: source, Language: req.Language, Script: req.Script, UserID: u.ID,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	jobID, err := s.jobs.Enqueue(r.Context(), kind, jobs.PriorityBackground, payload)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// 202, not 201: nothing has been created yet — the job is only queued,
	// and the resulting Text/Dialog row is created later, asynchronously, by
	// the process_text/process_dialog job handler.
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID})
}

// resolveIngestContent resolves req's raw content synchronously (pasted
// text, uploaded file content, or a server-side URL fetch) and the value
// that becomes the enqueued job's ingest_source field: the filename for
// "file", the URL for "url", empty for pasted "text".
func resolveIngestContent(ctx context.Context, req apiIngestRequest) (content, source string, err error) {
	switch req.Source {
	case ingestSourceText:
		if strings.TrimSpace(req.Content) == "" {
			return "", "", errors.New("content is required")
		}
		return req.Content, "", nil
	case ingestSourceFile:
		if strings.TrimSpace(req.Content) == "" {
			return "", "", errors.New("content is required")
		}
		return req.Content, req.Filename, nil
	case ingestSourceURL:
		if strings.TrimSpace(req.URL) == "" {
			return "", "", errors.New("url is required")
		}
		body, contentType, err := fetchIngestURL(ctx, req.URL)
		if err != nil {
			return "", "", err
		}
		text := string(body)
		if contentType == "text/html" || contentType == "application/xhtml+xml" {
			text = stripHTML(text)
		}
		return text, req.URL, nil
	default:
		return "", "", fmt.Errorf("source must be one of %q, %q, %q", ingestSourceURL, ingestSourceFile, ingestSourceText)
	}
}

// isDisallowedIngestTarget reports whether addr is a loopback, RFC1918/
// RFC4193-private, link-local (which already covers the literal cloud
// metadata address, 169.254.169.254, since 169.254.0.0/16 is itself
// link-local — checked explicitly anyway, as defense in depth against a
// future change to the link-local check above it), or unspecified address —
// every category of "internal infrastructure a server-side fetch must never
// reach" in one place, and unit-tested directly (see ingest_test.go) without
// needing a real socket.
func isDisallowedIngestTarget(addr netip.Addr) bool {
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsUnspecified() ||
		addr.String() == "169.254.169.254"
}

// rejectPrivateOrMetadataAddress is ingestDialer's Control hook: address is
// the literal "ip:port" of the connection about to be made — i.e. after DNS
// resolution, on the real IP, not the hostname the user supplied. Running
// the check here (rather than only parsing/validating the URL string up
// front) is what catches a public-looking hostname that resolves to a
// private IP (DNS rebinding) and a redirect Location header pointing
// somewhere new — fetchIngestURL's original scheme/host-string validation
// alone could not catch either.
func rejectPrivateOrMetadataAddress(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse dial address %q: %w", address, err)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("parse dial address host %q: %w", host, err)
	}
	if isDisallowedIngestTarget(addr) {
		return fmt.Errorf("refusing to connect to disallowed address %s", addr)
	}
	return nil
}

// ingestTransport is the http.Transport every URL-ingest fetch uses — its
// DialContext is backed by a net.Dialer whose Control hook is
// rejectPrivateOrMetadataAddress, so no ingest fetch (initial URL, DNS
// rebind, or redirect target) can ever reach loopback/private/link-local/
// metadata infrastructure.
var ingestTransport = &http.Transport{
	DialContext: (&net.Dialer{Control: rejectPrivateOrMetadataAddress}).DialContext,
}

// fetchIngestURL retrieves rawURL over HTTP(S) and returns its body and
// (media-type-only, parameters stripped) content type. The scheme is
// restricted to http/https, the response is rejected outright — rather than
// silently truncated — once it exceeds ingestFetchMaxBytes (see that
// constant's doc comment for why), redirects are capped at ingestMaxRedirects,
// and every connection (including one reached only via a redirect) is
// validated by ingestTransport's dialer against internal/metadata addresses.
func fetchIngestURL(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("parse url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, "", fmt.Errorf("url must use http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, "", errors.New("url has no host")
	}

	client := &http.Client{
		Timeout:   ingestFetchTimeout,
		Transport: ingestTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= ingestMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", ingestMaxRedirects)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		// Never echo the real error to the caller: it can distinguish
		// "connection refused" from "timed out" from "TLS error", which
		// turns this endpoint into a host/port-scanning oracle against
		// whatever the fetch could reach. Logged server-side instead, where
		// an operator can still see it.
		log.Printf("ingest: fetch url %s: %v", rawURL, err)
		return nil, "", errors.New("could not fetch the URL")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("fetch url: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, ingestFetchMaxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read response body: %w", err)
	}
	if len(body) > ingestFetchMaxBytes {
		return nil, "", fmt.Errorf("response exceeds %d byte limit", ingestFetchMaxBytes)
	}

	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")) //nolint:errcheck // an unparsable/missing Content-Type just falls through to the raw-body branch below
	return body, contentType, nil
}

// --- HTML-to-plain-text stripping ---
//
// Ported from knowledge/internal/ingest/html.go (same regexes, same
// strategy) rather than reinvented: a hand-rolled tag scanner, not a real
// HTML parser, is an accepted trade-off there and here alike — every
// ingested document eventually reaches a human via the resulting Text/Dialog
// row, so best-effort plain-text extraction is good enough. Go's RE2 engine
// has no backreferences, so the two script/style element types are spelled
// out separately below rather than matched with one captured tag name (see
// specs/memory.md's [gotcha] entry for this exact trap).

// scriptOrStyleBlock matches a <script>...</script> or <style>...</style>
// element, tag and content together, case-insensitively, so neither the
// markup nor any embedded JS/CSS (which may itself contain "<" or ">")
// leaks into the stripped output.
var scriptOrStyleBlock = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>|<style\b[^>]*>.*?</style\s*>`)

// htmlComment matches an HTML comment, including its delimiters.
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// blockClosingTag matches the closing tags and self-closing breaks that mark
// the end of a block-level element; these become a newline rather than
// being dropped, so paragraph/line structure survives as plain text.
var blockClosingTag = regexp.MustCompile(`(?i)</\s*(p|div|h[1-6]|li)\s*>|<\s*br\s*/?\s*>`)

// anyTag matches any remaining HTML tag once script/style/comment blocks and
// block-closing tags have already been handled.
var anyTag = regexp.MustCompile(`<[^>]*>`)

// runOfSpaces matches two or more consecutive spaces or tabs.
var runOfSpaces = regexp.MustCompile(`[ \t]{2,}`)

// runOfNewlines matches three or more consecutive newlines.
var runOfNewlines = regexp.MustCompile(`\n{3,}`)

// stripHTML converts an HTML string into plain text: <script>/<style>
// blocks and comments are removed entirely, block-level closing tags become
// newlines, every other tag is dropped, entities are unescaped, and excess
// whitespace is collapsed.
func stripHTML(source string) string {
	text := scriptOrStyleBlock.ReplaceAllString(source, "")
	text = htmlComment.ReplaceAllString(text, "")
	text = blockClosingTag.ReplaceAllString(text, "\n")
	text = anyTag.ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	text = runOfSpaces.ReplaceAllString(text, " ")
	text = runOfNewlines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

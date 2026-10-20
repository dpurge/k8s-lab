package modelcfg

import (
	"fmt"
	"net/http"
	"os"
	pathpkg "path"
	"sort"
	"strings"
)

// reservedHeaders are the names that other settings own, with where to set
// them. Lower case.
var reservedHeaders = map[string]string{
	"authorization":     "use api_key_from",
	"x-api-key":         "use api_key_from",
	"content-type":      "the provider sets it",
	"content-length":    "the HTTP client sets it",
	"host":              "the HTTP client sets it from base_url",
	"connection":        "the HTTP client manages it",
	"transfer-encoding": "the HTTP client manages it",
}

// isHeaderToken says whether s is a valid HTTP header name (an RFC 7230 token).
func isHeaderToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// headerSource splits a header value into how it is read. For a literal value
// kind is keyNone and arg is the value itself; ok is false for a malformed env:
// or file: form.
func headerSource(value string) (kind keyKind, arg string, ok bool) {
	switch {
	case strings.HasPrefix(value, "env:"):
		name := strings.TrimPrefix(value, "env:")
		return keyEnv, name, envNamePattern.MatchString(name)
	case strings.HasPrefix(value, "file:"):
		path := strings.TrimPrefix(value, "file:")
		return keyFile, path, pathpkg.IsAbs(path) && !strings.ContainsRune(path, 0)
	}
	return keyNone, value, !strings.ContainsAny(value, "\r\n\x00")
}

// lintHeaders checks the headers of a provider. Messages name the provider and
// the header name, never a value: a literal value may be a pasted secret.
func lintHeaders(p Provider, hasKey bool, report func(name, message string)) {
	if p.Type == "demo" {
		if hasKey {
			report("", fmt.Sprintf("provider %q is a demo provider, which takes no headers: it answers from inside the assistant", p.ID))
		}
		return
	}
	names := make([]string, 0, len(p.Headers))
	for name := range p.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := map[string]string{}
	for _, name := range names {
		if !isHeaderToken(name) {
			report(name, fmt.Sprintf("provider %q: header name %q is not a valid HTTP header name", p.ID, name))
			continue
		}
		lower := strings.ToLower(name)
		if first, dup := seen[lower]; dup {
			report(name, fmt.Sprintf("provider %q: header %q repeats header %q: header names are not case sensitive", p.ID, name, first))
			continue
		}
		seen[lower] = name
		if where, reserved := reservedHeaders[lower]; reserved {
			report(name, fmt.Sprintf("provider %q: header %q is not allowed here: %s", p.ID, name, where))
			continue
		}
		if _, _, ok := headerSource(p.Headers[name]); ok {
			continue
		}
		switch {
		case strings.HasPrefix(p.Headers[name], "env:"):
			report(name, fmt.Sprintf("provider %q: header %q: env:NAME needs a valid environment variable name", p.ID, name))
		case strings.HasPrefix(p.Headers[name], "file:"):
			report(name, fmt.Sprintf("provider %q: header %q: file: needs an absolute path", p.ID, name))
		default:
			report(name, fmt.Sprintf("provider %q: header %q: a literal value must not contain a line break or NUL", p.ID, name))
		}
	}
}

// ResolvedHeaders is a provider's headers as they resolved at one moment. Like
// Credential, printing one shows sources, never values.
type ResolvedHeaders struct {
	entries []resolvedHeader // sorted by name
}

type resolvedHeader struct {
	name string
	// source is "env:NAME" or "file:/path"; empty for a literal.
	source  string
	value   string
	present bool
}

// ResolveHeaders reads the headers of p afresh, from the environment (getenv)
// and from files (readFile); a nil function means os.Getenv or os.ReadFile. An
// env: or file: value is trimmed of whitespace; one that is unset, empty,
// unreadable or holds a line break is not present. A literal is always present.
func ResolveHeaders(p Provider, getenv func(string) string, readFile func(string) ([]byte, error)) ResolvedHeaders {
	if getenv == nil {
		getenv = os.Getenv
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	var h ResolvedHeaders
	for name, raw := range p.Headers {
		kind, arg, ok := headerSource(raw)
		e := resolvedHeader{name: name}
		switch {
		case kind == keyNone:
			e.value, e.present = raw, true
		case !ok:
			e.source = raw[:strings.Index(raw, ":")+1] + "(invalid)"
		case kind == keyEnv:
			e.source = raw
			e.value = strings.TrimSpace(getenv(arg))
		case kind == keyFile:
			e.source = raw
			if b, err := readFile(arg); err == nil {
				e.value = strings.TrimSpace(string(b))
			}
		}
		if kind != keyNone {
			e.present = ok && e.value != "" && !strings.ContainsAny(e.value, "\r\n\x00")
			if !e.present {
				e.value = ""
			}
		}
		h.entries = append(h.entries, e)
	}
	sort.Slice(h.entries, func(i, j int) bool { return h.entries[i].name < h.entries[j].name })
	return h
}

// String lists the header names, not the values.
func (h ResolvedHeaders) String() string {
	names := make([]string, len(h.entries))
	for i, e := range h.entries {
		names[i] = e.name
	}
	return strings.Join(names, ",")
}

// GoString keeps %#v from printing the values.
func (h ResolvedHeaders) GoString() string {
	return fmt.Sprintf("modelcfg.ResolvedHeaders{%s}", h.String())
}

// Usable is false when a header read from the environment or a file has no
// value now.
func (h ResolvedHeaders) Usable() bool { return len(h.Missing()) == 0 }

// Missing names each header without a value, as "header NAME from SOURCE". It
// is safe for server logs and for the error text of a failed call.
func (h ResolvedHeaders) Missing() []string {
	var out []string
	for _, e := range h.entries {
		if !e.present {
			out = append(out, fmt.Sprintf("header %s from %s", e.name, e.source))
		}
	}
	return out
}

// Require returns nil, or an error that names the missing headers and their
// sources but never a value.
func (h ResolvedHeaders) Require(providerID string) error {
	if missing := h.Missing(); len(missing) > 0 {
		return fmt.Errorf("provider %q cannot send %s: not set, unreadable or empty", providerID, strings.Join(missing, ", "))
	}
	return nil
}

// Map returns the header values by name, for a client that takes them as a map;
// nil when there are none. It is the only way the values leave this type besides Apply.
func (h ResolvedHeaders) Map() map[string]string {
	if len(h.entries) == 0 {
		return nil
	}
	m := make(map[string]string, len(h.entries))
	for _, e := range h.entries {
		m[e.name] = e.value
	}
	return m
}

// Apply sets every header on the request, replacing a header of the same name
// the provider set as its default. Call it after the provider's own headers.
func (h ResolvedHeaders) Apply(r *http.Request) {
	for _, e := range h.entries {
		r.Header.Set(e.name, e.value)
	}
}

// HeaderSources lists "header NAME from SOURCE" for every header of p read from
// the environment or a file, in name order. It is for log lines.
func HeaderSources(p Provider) []string {
	var out []string
	for _, e := range ResolveHeaders(p, func(string) string { return "" }, func(string) ([]byte, error) { return nil, os.ErrNotExist }).entries {
		if e.source != "" {
			out = append(out, fmt.Sprintf("header %s from %s", e.name, e.source))
		}
	}
	return out
}

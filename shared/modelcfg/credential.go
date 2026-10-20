package modelcfg

import (
	"fmt"
	"os"
	"strings"
)

// Credential is what a provider's api_key_from resolved to at
// one moment. Its String form is the source, never the value, so printing one
// by accident cannot leak the secret.
type Credential struct {
	// Configured: the provider names a source in api_key_from (env:NAME or file:/path).
	Configured bool
	// Present: the source holds a non-blank value right now. A configured
	// credential that is not present is unset, empty, an unreadable file, or has
	// a CR, LF or NUL inside it.
	Present bool
	// Source is the provider's api_key_from text ("env:NAME" or "file:/path");
	// empty when nothing is configured. It is safe for server logs, never for a
	// response to a client.
	Source string

	value string
}

// String is the source, not the value.
func (c Credential) String() string { return c.Source }

// GoString keeps %#v from printing the value.
func (c Credential) GoString() string {
	return fmt.Sprintf("modelcfg.Credential{Source: %q, Present: %t}", c.Source, c.Present)
}

// Value is the credential, trimmed of surrounding whitespace; empty unless
// Present. It must never be logged or put in an error or a response.
func (c Credential) Value() string { return c.value }

// ResolveCredential reads the credential of p according to its api_key_from:
// from the environment variable env:NAME names (getenv) or from the file
// file:/path names (readFile). It reads the source afresh on every call; a rotated
// file needs no restart, but an environment variable of a running process does
// not change, so a new value there needs a restart. A nil getenv or
// readFile means os.Getenv or os.ReadFile. A malformed value (the lint refuses
// it at load) counts as configured but absent.
func ResolveCredential(p Provider, getenv func(string) string, readFile func(string) ([]byte, error)) Credential {
	if getenv == nil {
		getenv = os.Getenv
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	kind, arg, ok := parseKeyFrom(p.APIKeyFrom)
	if kind == keyNone && ok {
		return Credential{}
	}
	c := Credential{Configured: true, Source: p.APIKeyFrom}
	switch {
	case !ok:
	case kind == keyFile:
		if raw, err := readFile(arg); err == nil {
			c.value = strings.TrimSpace(string(raw))
		}
	case kind == keyEnv:
		c.value = strings.TrimSpace(getenv(arg))
	}
	// A value with a line break or NUL cannot be sent in a header (net/http
	// refuses it), so it counts as missing, here and so in the availability of
	// the provider; the same rule ResolveHeaders applies to a header value.
	c.Present = c.value != "" && !strings.ContainsAny(c.value, "\r\n\x00")
	if !c.Present {
		c.value = ""
	}
	return c
}

// CredentialRequired says whether a provider of this type is unusable without a
// credential: anthropic and vertex are; ollama and openai may be local servers
// that need none, and demo answers from inside the process.
func (p Provider) CredentialRequired() bool {
	return p.Type == "anthropic" || p.Type == "vertex"
}

// CredentialUsable is false for a provider that requires a credential and has
// none present now, and for one with an env: or file: header that has no value
// now (see ResolveHeaders).
func CredentialUsable(p Provider, getenv func(string) string, readFile func(string) ([]byte, error)) bool {
	if p.CredentialRequired() && !ResolveCredential(p, getenv, readFile).Present {
		return false
	}
	return ResolveHeaders(p, getenv, readFile).Usable()
}

// Require returns the value, or an error that names the source but never the
// value. A provider that does not require a credential calls it only when one
// is configured.
func (c Credential) Require(providerID string) (string, error) {
	switch {
	case !c.Configured:
		return "", fmt.Errorf("provider %q has no credential: set api_key_from to env:NAME or file:/absolute/path", providerID)
	case !c.Present:
		return "", fmt.Errorf("the credential of provider %q (%s) is not set, unreadable or empty", providerID, c.Source)
	}
	return c.value, nil
}

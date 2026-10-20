package modelcfg

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const (
	headerSecretLiteral = "literal-SECRET-HEADER-VALUE"
	headerSecretEnv     = "env-SECRET-HEADER-VALUE"
	headerSecretFile    = "file-SECRET-HEADER-VALUE"
)

// withHeaders is one provider of the type with api_key_from and the given
// headers block (indented lines under "headers:").
func withHeaders(typ, headerLines string) string {
	doc := providerDoc(typ, `api_key_from: ""`)
	if typ == "demo" {
		doc = "providers:\n  - id: p\n    type: demo\n" + "models:\n  - {id: m, provider: p, model: x, tags: [chat], priority: 1}\n"
	}
	return strings.Replace(doc, "models:\n", "    headers:\n"+headerLines+"models:\n", 1)
}

func TestEveryHTTPProviderTypeAcceptsHeadersInAllThreeForms(t *testing.T) {
	lines := "      X-Literal: plain\n      X-Env: env:SOME_VAR\n      x-file: file:/run/secrets/h\n      anthropic-version: 2023-06-01\n      HTTP-Referer: https://example.test\n      api-key: env:K\n"
	for _, typ := range credentialTypes {
		c, err := load(t, withHeaders(typ, lines))
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if got := len(c.Providers[0].Headers); got != 6 {
			t.Errorf("%s: %d headers decoded", typ, got)
		}
	}
}

func TestADemoProviderRejectsHeaders(t *testing.T) {
	e := loadError(t, withHeaders("demo", "      X-A: b\n"))
	f := finding(t, e, "providers.0.headers")
	if !strings.Contains(f.Message, `"p"`) || !strings.Contains(f.Message, "headers") {
		t.Errorf("message = %q", f.Message)
	}
}

func TestHeaderLintRules(t *testing.T) {
	cases := []struct {
		name, line, path, want string
	}{
		{"empty name", `      "": v`, "providers.0.headers", "not a valid HTTP header name"},
		{"space in name", `      "X Foo": v`, "providers.0.headers.X Foo", "not a valid HTTP header name"},
		{"colon in name", `      "X:Foo": v`, "providers.0.headers.X:Foo", "not a valid HTTP header name"},
		{"authorization", "      Authorization: x", "providers.0.headers.Authorization", "use api_key_from"},
		{"x-api-key", "      X-Api-Key: x", "providers.0.headers.X-Api-Key", "use api_key_from"},
		{"content-type", "      content-type: x", "providers.0.headers.content-type", "not allowed"},
		{"content-length", "      Content-Length: \"1\"", "providers.0.headers.Content-Length", "not allowed"},
		{"host", "      HOST: x", "providers.0.headers.HOST", "base_url"},
		{"connection", "      Connection: x", "providers.0.headers.Connection", "not allowed"},
		{"transfer-encoding", "      Transfer-Encoding: x", "providers.0.headers.Transfer-Encoding", "not allowed"},
		{"bad env name", "      X-A: env:1BAD", "providers.0.headers.X-A", "environment variable name"},
		{"empty env name", `      X-A: "env:"`, "providers.0.headers.X-A", "environment variable name"},
		{"relative file", "      X-A: file:relative/path", "providers.0.headers.X-A", "absolute path"},
		{"empty file", `      X-A: "file:"`, "providers.0.headers.X-A", "absolute path"},
		{"line break in literal", `      X-A: "a\r\nInjected: b"`, "providers.0.headers.X-A", "line break"},
		{"nul in literal", `      X-A: "a\u0000b"`, "providers.0.headers.X-A", "NUL"},
	}
	for _, tc := range cases {
		e := loadError(t, withHeaders("openai", tc.line+"\n"))
		f := finding(t, e, tc.path)
		if !strings.Contains(f.Message, tc.want) || !strings.Contains(f.Message, `"p"`) {
			t.Errorf("%s: message %q lacks %q or the provider id", tc.name, f.Message, tc.want)
		}
		if f.Line == 0 {
			t.Errorf("%s: no line", tc.name)
		}
	}
}

func TestCaseInsensitiveDuplicateHeadersAreAFinding(t *testing.T) {
	e := loadError(t, withHeaders("anthropic", "      X-Foo: a\n      x-foo: b\n"))
	found := false
	for _, f := range e.Findings {
		if strings.Contains(f.Message, "repeats") && strings.Contains(f.Message, `"p"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("no duplicate finding:\n%s", e)
	}
}

func TestHeaderFindingsNeverEchoAValue(t *testing.T) {
	lines := fmt.Sprintf("      Authorization: %s\n      X-Env: env:%s bad\n      X-File: file:%s\n      \"Bad Name\": %s\n", headerSecretLiteral, headerSecretEnv, headerSecretFile, headerSecretLiteral)
	e := loadError(t, withHeaders("openai", lines))
	if len(e.Findings) < 4 {
		t.Fatalf("findings: %s", e)
	}
	for _, secret := range []string{headerSecretLiteral, headerSecretEnv, headerSecretFile} {
		if strings.Contains(e.Error(), secret) {
			t.Errorf("a finding echoes %q:\n%s", secret, e)
		}
	}
}

func TestResolveHeadersLiteralEnvAndFileAreTrimmedAndRereadEveryCall(t *testing.T) {
	files := map[string]string{"/run/h": "  file-v1\n"}
	p := Provider{ID: "p", Type: "openai", Headers: map[string]string{
		"X-Literal": "plain", "X-Env": "env:HV", "X-File": "file:/run/h"}}
	env := map[string]string{"HV": " env-v1 \n"}
	h := ResolveHeaders(p, fakeEnv(env), fakeFiles(files))
	if !h.Usable() {
		t.Fatalf("missing: %v", h.Missing())
	}
	if m := h.Map(); m["X-Literal"] != "plain" || m["X-Env"] != "env-v1" || m["X-File"] != "file-v1" {
		t.Errorf("first = %v", m)
	}
	env["HV"], files["/run/h"] = "env-v2", "file-v2"
	if m := ResolveHeaders(p, fakeEnv(env), fakeFiles(files)).Map(); m["X-Env"] != "env-v2" || m["X-File"] != "file-v2" {
		t.Errorf("rotated values must be re-read: %v", m)
	}
}

func TestAnEmptyOrUnreadableHeaderSourceIsMissingAndNamesNameAndSource(t *testing.T) {
	p := Provider{ID: "p", Type: "ollama", Headers: map[string]string{
		"X-Unset": "env:UNSET", "X-Blank": "env:BLANK", "X-Gone": "file:/nope", "X-Empty": "file:/empty", "X-Multi": "env:MULTI", "X-Ok": "plain"}}
	env := map[string]string{"BLANK": " \n", "MULTI": "a\r\nb"}
	h := ResolveHeaders(p, fakeEnv(env), fakeFiles(map[string]string{"/empty": "\n"}))
	if h.Usable() {
		t.Fatal("must not be usable")
	}
	want := []string{"header X-Blank from env:BLANK", "header X-Empty from file:/empty", "header X-Gone from file:/nope", "header X-Multi from env:MULTI", "header X-Unset from env:UNSET"}
	if got := h.Missing(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("missing = %v, want %v", got, want)
	}
	err := h.Require("p")
	if err == nil || !strings.Contains(err.Error(), `"p"`) || !strings.Contains(err.Error(), "header X-Unset from env:UNSET") {
		t.Errorf("Require = %v", err)
	}
	if CredentialUsable(p, fakeEnv(env), nil) {
		t.Error("a provider with a missing header is not usable")
	}
}

func TestNoHeadersIsUsableAndMapIsNil(t *testing.T) {
	h := ResolveHeaders(Provider{Type: "ollama"}, nil, nil)
	if !h.Usable() || h.Map() != nil || h.Require("p") != nil {
		t.Errorf("empty headers: %#v", h)
	}
}

func TestResolvedHeadersNeverPrintValues(t *testing.T) {
	p := Provider{ID: "p", Headers: map[string]string{"X-A": headerSecretLiteral, "X-B": "env:B", "X-C": "file:/c"}}
	h := ResolveHeaders(p, fakeEnv(map[string]string{"B": headerSecretEnv}), fakeFiles(map[string]string{"/c": headerSecretFile}))
	out := fmt.Sprintf("%v %+v %#v %s", h, h, h, h)
	for _, secret := range []string{headerSecretLiteral, headerSecretEnv, headerSecretFile} {
		if strings.Contains(out, secret) {
			t.Errorf("printed %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "X-A") {
		t.Errorf("names are expected: %s", out)
	}
}

func TestApplyReplacesADefaultCaseInsensitively(t *testing.T) {
	r, _ := http.NewRequest("POST", "http://x.test", nil)
	r.Header.Set("anthropic-version", "default")
	ResolveHeaders(Provider{Headers: map[string]string{"ANTHROPIC-VERSION": "custom", "x-title": "t"}}, nil, nil).Apply(r)
	if got := r.Header.Values("Anthropic-Version"); len(got) != 1 || got[0] != "custom" {
		t.Errorf("anthropic-version = %v", got)
	}
	if r.Header.Get("X-Title") != "t" {
		t.Errorf("x-title = %q", r.Header.Get("X-Title"))
	}
}

func TestHeaderSourcesListsOnlyEnvAndFileHeadersInNameOrder(t *testing.T) {
	p := Provider{Headers: map[string]string{"X-Z": "env:Z", "X-A": "file:/a", "X-Lit": headerSecretLiteral}}
	got := strings.Join(HeaderSources(p), "|")
	if got != "header X-A from file:/a|header X-Z from env:Z" {
		t.Errorf("sources = %q", got)
	}
}

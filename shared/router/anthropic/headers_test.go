package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

const (
	headerLiteral = "literal-SECRET-HEADER-VALUE"
	headerEnvVal  = "env-SECRET-HEADER-VALUE"
	headerFileVal = "file-SECRET-HEADER-VALUE"
)

// headerServer answers like the Messages API and records the headers of every request.
func headerServer(t *testing.T) (*httptest.Server, func() []http.Header) {
	t.Helper()
	var mu sync.Mutex
	var seen []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okBody)
	}))
	t.Cleanup(server.Close)
	return server, func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		return append([]http.Header(nil), seen...)
	}
}

// headerRig is a provider whose headers come from a literal, the environment
// and a file, with every source under the test's control.
func headerRig(t *testing.T, url string, headers map[string]string) (*Provider, modelcfg.Provider, map[string]string, map[string]string) {
	t.Helper()
	env := map[string]string{"HDR_ENV": headerEnvVal, "KEY_ENV": "the-key"}
	files := map[string]string{"/h/file": headerFileVal + "\n", "/h/token": "the-token"}
	read := func(path string) ([]byte, error) {
		if v, ok := files[path]; ok {
			return []byte(v), nil
		}
		return nil, io.ErrUnexpectedEOF
	}
	p := New(WithGetenv(func(n string) string { return env[n] }), WithReadFile(read))
	prov := modelcfg.Provider{ID: "claude", Type: "anthropic", BaseURL: url, APIKeyFrom: "env:KEY_ENV"}
	prov.Headers = headers
	return p, prov, env, files
}

func TestCustomHeadersAreSentFromLiteralEnvAndFileAndReReadEveryCall(t *testing.T) {
	server, seen := headerServer(t)
	p, prov, _, _ := headerRig(t, server.URL, map[string]string{"X-Title": headerLiteral, "x-from-env": "env:HDR_ENV", "X-From-File": "file:/h/file"})
	if _, err := p.Complete(context.Background(), prov, model, router.Request{Input: "{}"}); err != nil {
		t.Fatal(err)
	}
	got := seen()[0]
	if got.Get("X-Title") != headerLiteral || got.Get("X-From-Env") != headerEnvVal || got.Get("X-From-File") != headerFileVal {
		t.Error("a literal, env or file header is missing or wrong (values not shown)")
	}
}

func TestRotatedHeaderSourcesAreReReadOnTheNextCall(t *testing.T) {
	server, seen := headerServer(t)
	p, prov, env, files := headerRig(t, server.URL, map[string]string{"X-From-Env": "env:HDR_ENV", "X-From-File": "file:/h/file"})
	for i, value := range []string{"one", "two"} {
		env["HDR_ENV"], files["/h/file"] = "env-"+value, "file-"+value
		if _, err := p.Complete(context.Background(), prov, model, router.Request{Input: "{}"}); err != nil {
			t.Fatal(err)
		}
		got := seen()[i]
		if got.Get("X-From-Env") != "env-"+value || got.Get("X-From-File") != "file-"+value {
			t.Errorf("call %d did not use the rotated values", i)
		}
	}
}

func TestACustomHeaderReplacesTheDefaultAnthropicVersionCaseInsensitively(t *testing.T) {
	server, seen := headerServer(t)
	p, prov, _, _ := headerRig(t, server.URL, map[string]string{"Anthropic-VERSION": "2099-01-01", "anthropic-beta": "some-beta"})
	if _, err := p.Complete(context.Background(), prov, model, router.Request{Input: "{}"}); err != nil {
		t.Fatal(err)
	}
	got := seen()[0]
	if v := got.Values("Anthropic-Version"); len(v) != 1 || v[0] != "2099-01-01" {
		t.Errorf("anthropic-version = %v", v)
	}
	if got.Get("Anthropic-Beta") != "some-beta" || got.Get("X-Api-Key") != "the-key" {
		t.Error("beta header or the key is missing")
	}
}

func TestAnUnsetHeaderVariableIsAClientFailureNamingHeaderAndSourceAndNoRequestIsMade(t *testing.T) {
	server, seen := headerServer(t)
	p, prov, env, _ := headerRig(t, server.URL, map[string]string{"X-From-Env": "env:HDR_ENV", "X-Lit": headerLiteral})
	delete(env, "HDR_ENV")
	_, err := p.Complete(context.Background(), prov, model, router.Request{Input: "{}"})
	f := failure(t, err)
	if f.Kind != router.FailClient {
		t.Errorf("kind = %s", f.Kind)
	}
	for _, want := range []string{"header X-From-Env from env:HDR_ENV", "cannot send"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), headerLiteral) || strings.Contains(err.Error(), headerEnvVal) {
		t.Error("the error shows a header value")
	}
	if n := len(seen()); n != 0 {
		t.Errorf("%d requests were made", n)
	}
}

func TestAnUnreadableHeaderFileIsAClientFailure(t *testing.T) {
	server, seen := headerServer(t)
	p, prov, _, files := headerRig(t, server.URL, map[string]string{"X-From-File": "file:/h/file"})
	delete(files, "/h/file")
	_, err := p.Complete(context.Background(), prov, model, router.Request{Input: "{}"})
	if f := failure(t, err); f.Kind != router.FailClient || !strings.Contains(err.Error(), "header X-From-File from file:/h/file") {
		t.Errorf("err = %v", err)
	}
	if len(seen()) != 0 {
		t.Error("a request was made")
	}
}

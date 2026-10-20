package router_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

const headerAvailabilityConfig = `providers:
  - id: gpu
    type: ollama
    base_url: "http://gpu.test:11434"
    api_key_from: ""
    headers:
      X-Foo: env:HDR_VAR
      X-Lit: literal-HEADER-VALUE-NEVER-LOGGED
  - {id: cpu, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
models:
  - {id: big, provider: gpu, model: big-model, tags: [chat], priority: 30}
  - {id: small, provider: cpu, model: small-model, tags: [chat], priority: 10}
`

const headerEnvSecret = "env-HEADER-VALUE-NEVER-LOGGED"

type mutableEnv struct {
	mu   sync.Mutex
	vars map[string]string
}

func (e *mutableEnv) set(name, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.vars == nil {
		e.vars = map[string]string{}
	}
	e.vars[name] = value
}

func (e *mutableEnv) get(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.vars[name]
}

func TestAProviderWithAnUnsetHeaderVariableIsSkippedLikeAMissingCredential(t *testing.T) {
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(headerAvailabilityConfig))
	if err != nil {
		t.Fatal(err)
	}
	env := &mutableEnv{}
	provider := routertest.NewProvider(nil)
	buf := &bytes.Buffer{}
	r, err := router.NewRouter(cfg, map[string]router.Provider{"ollama": provider},
		router.WithClock(routertest.NewClock(t0)),
		router.WithAvailability(router.CredentialAvailability(env.get, nil)),
		router.WithLogger(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		got, err := r.Select([]string{"chat"})
		if err != nil || ids(got) != "small" {
			t.Fatalf("got %q, %v: the fallback must serve", ids(got), err)
		}
	}
	if st := r.Status()[0]; st.ID != "big" || st.Available || st.Reason != router.ReasonMissingCredentials {
		t.Errorf("big = %+v", st)
	}
	if _, err := r.Complete(context.Background(), router.Request{Input: "{}", Tags: []string{"chat"}}); err != nil {
		t.Fatal(err)
	}
	if calls := provider.Calls(); len(calls) != 1 || calls[0].ModelID != "small" {
		t.Errorf("calls = %+v", calls)
	}

	warns := countLines(t, buf, "llm provider unavailable")
	if len(warns) != 1 {
		t.Fatalf("warnings = %d, want exactly 1 (once per transition)", len(warns))
	}
	w := warns[0]
	if w.str("level") != "WARN" || w.str("provider_id") != "gpu" || w.str("reason") != "missing_credentials" ||
		w.str("credential_source") != "none; header X-Foo from env:HDR_VAR" {
		t.Errorf("warning = %v", w)
	}

	env.set("HDR_VAR", headerEnvSecret)
	got, _ := r.Select([]string{"chat"})
	if ids(got) != "big,small" {
		t.Errorf("after the variable appears: %q", ids(got))
	}
	if n := len(countLines(t, buf, "llm provider available")); n != 1 {
		t.Errorf("recovery lines = %d, want 1", n)
	}
	env.set("HDR_VAR", "")
	r.Select([]string{"chat"})
	if n := len(countLines(t, buf, "llm provider unavailable")); n != 2 {
		t.Errorf("warnings after losing it again = %d, want 2", n)
	}

	for _, secret := range []string{headerEnvSecret, "literal-HEADER-VALUE-NEVER-LOGGED"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("a log line holds a header value %q", secret)
		}
	}
}

// countLines parses the JSON log lines with the message.
func countLines(t *testing.T, buf *bytes.Buffer, msg string) []logLine {
	t.Helper()
	rig := &availabilityRig{buf: buf}
	return rig.lines(t, msg)
}

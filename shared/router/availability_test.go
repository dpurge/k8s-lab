package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

const availabilityConfig = `providers:
  - {id: claude, type: anthropic, api_key_from: file:/run/secrets/claude-key}
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
models:
  - {id: opus, provider: claude, model: claude-x, tags: [chat, deep], priority: 30}
  - {id: small, provider: laptop, model: gemma4:e4b, tags: [chat], priority: 10}
routing:
  breaker: {failures: 1, cooldown_seconds: 60, rate_limited_cooldown_seconds: 30}
`

const fileSecret = "sk-ant-FILE-CONTENT-NEVER-LOGGED"

// secretFiles is a file system whose files tests can create and remove.
type secretFiles struct {
	mu    sync.Mutex
	files map[string]string
}

func (s *secretFiles) set(path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = map[string]string{}
	}
	s.files[path] = content
}

func (s *secretFiles) remove(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, path)
}

func (s *secretFiles) read(path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if content, ok := s.files[path]; ok {
		return []byte(content), nil
	}
	return nil, errors.New("no such file")
}

func firstScript(scripts []map[string][]routertest.Result) map[string][]routertest.Result {
	if len(scripts) == 0 {
		return nil
	}
	return scripts[0]
}

type availabilityRig struct {
	router   *router.Router
	provider *routertest.Provider
	clock    *routertest.Clock
	files    *secretFiles
	buf      *bytes.Buffer
}

func newAvailabilityRig(t *testing.T, haveKey bool, scripts ...map[string][]routertest.Result) *availabilityRig {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(availabilityConfig))
	if err != nil {
		t.Fatal(err)
	}
	rig := &availabilityRig{provider: routertest.NewProvider(firstScript(scripts)), clock: routertest.NewClock(t0), files: &secretFiles{}, buf: &bytes.Buffer{}}
	if haveKey {
		rig.files.set("/run/secrets/claude-key", fileSecret+"\n")
	}
	rig.router, err = router.NewRouter(cfg, map[string]router.Provider{"ollama": rig.provider, "anthropic": rig.provider},
		router.WithClock(rig.clock),
		router.WithAvailability(router.CredentialAvailability(nil, rig.files.read)),
		router.WithLogger(slog.New(slog.NewJSONHandler(rig.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if err != nil {
		t.Fatal(err)
	}
	return rig
}

func (a *availabilityRig) lines(t *testing.T, msg string) []logLine {
	t.Helper()
	var out []logLine
	for _, raw := range strings.Split(strings.TrimSpace(a.buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line logLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatal(err)
		}
		if line.str("msg") == msg {
			out = append(out, line)
		}
	}
	return out
}

func TestAProviderWithoutItsCredentialIsSkippedAndTheNextModelServes(t *testing.T) {
	rig := newAvailabilityRig(t, false)
	got, err := rig.router.Select([]string{"chat"})
	if err != nil || ids(got) != "small" {
		t.Fatalf("got %q, %v", ids(got), err)
	}
	if _, err := rig.router.Complete(context.Background(), router.Request{Input: "{}", Tags: []string{"chat"}}); err != nil {
		t.Fatal(err)
	}
	if calls := rig.provider.Calls(); len(calls) != 1 || calls[0].ModelID != "small" {
		t.Errorf("calls = %+v", calls)
	}
	candidates := rig.lines(t, "llm candidates")
	if len(candidates) != 1 {
		t.Fatalf("candidates lines = %d", len(candidates))
	}
	skipped, _ := candidates[0]["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v", candidates[0]["skipped"])
	}
	entry := skipped[0].(map[string]any)
	if entry["model_id"] != "opus" || entry["reason"] != router.ReasonMissingCredentials {
		t.Errorf("skipped entry = %v", entry)
	}
}

func TestNoModelErrorListsTheReasonPerSkippedModel(t *testing.T) {
	rig := newAvailabilityRig(t, false)
	_, err := rig.router.Select([]string{"deep"})
	var noModel *router.NoModelError
	if !errors.As(err, &noModel) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "opus is unavailable (missing_credentials)") || !strings.Contains(err.Error(), "small lacks the tags") {
		t.Errorf("err = %v", err)
	}
}

func TestStatusSaysWhyAModelIsNotAvailable(t *testing.T) {
	rig := newAvailabilityRig(t, false)
	status := rig.router.Status()
	if status[0].ID != "opus" || status[0].Available || status[0].Reason != router.ReasonMissingCredentials || !status[0].Until.IsZero() {
		t.Errorf("opus = %+v", status[0])
	}
	if !status[1].Available || status[1].Reason != "" {
		t.Errorf("small = %+v", status[1])
	}

}

func TestStatusSaysCooldownForAModelTheBreakerHolds(t *testing.T) {
	rig := newAvailabilityRig(t, true, map[string][]routertest.Result{"opus": {routertest.Fail(router.FailServer, "boom")}})
	rig.router.Complete(context.Background(), router.Request{Input: "{}", Tags: []string{"chat"}})
	st := rig.router.Status()[0]
	if st.Available || st.Reason != router.ReasonCooldown || st.Until.IsZero() {
		t.Errorf("opus after a failure = %+v", st)
	}
}

func TestAvailabilityIsLoggedOncePerTransitionNeverPerCallAndNeverWithSecrets(t *testing.T) {
	rig := newAvailabilityRig(t, false)

	// Unusable at construction: one warning, before any call.
	warns := rig.lines(t, "llm provider unavailable")
	if len(warns) != 1 {
		t.Fatalf("startup warnings = %d, want 1", len(warns))
	}
	w := warns[0]
	if w.str("level") != "WARN" || w.str("provider_id") != "claude" || w.str("reason") != "missing_credentials" ||
		w.str("credential_source") != "file:/run/secrets/claude-key" || strings.Join(w.list("models"), ",") != "opus" {
		t.Errorf("warning = %v", w)
	}

	for i := 0; i < 3; i++ {
		rig.router.Select([]string{"chat"})
		rig.router.Status()
		rig.router.Complete(context.Background(), router.Request{Input: "{}", Tags: []string{"chat"}})
	}
	if n := len(rig.lines(t, "llm provider unavailable")); n != 1 {
		t.Errorf("warnings after many calls = %d, want still 1", n)
	}
	if n := len(rig.lines(t, "llm provider available")); n != 0 {
		t.Errorf("recovery lines before recovery = %d", n)
	}

	// The key appears: recovery is logged once and the model is used.
	rig.files.set("/run/secrets/claude-key", fileSecret)
	for i := 0; i < 3; i++ {
		got, _ := rig.router.Select([]string{"chat"})
		if ids(got) != "opus,small" {
			t.Fatalf("after recovery got %q", ids(got))
		}
	}
	recovered := rig.lines(t, "llm provider available")
	if len(recovered) != 1 || recovered[0].str("level") != "INFO" || recovered[0].str("provider_id") != "claude" {
		t.Errorf("recovery lines = %v", recovered)
	}

	// Lost again: one more warning.
	rig.files.remove("/run/secrets/claude-key")
	rig.router.Select([]string{"chat"})
	rig.router.Select([]string{"chat"})
	if n := len(rig.lines(t, "llm provider unavailable")); n != 2 {
		t.Errorf("warnings after losing the key again = %d, want 2", n)
	}

	if strings.Contains(rig.buf.String(), fileSecret) {
		t.Error("a log line holds the content of the credential file")
	}
}

func TestAProviderThatNeedsNoCredentialIsNeverUnavailable(t *testing.T) {
	rig := newAvailabilityRig(t, false)
	if len(rig.lines(t, "llm provider unavailable")) != 1 {
		t.Fatalf("only claude may be reported: %s", rig.buf.String())
	}
	for _, st := range rig.router.Status() {
		if st.Provider == "laptop" && !st.Available {
			t.Errorf("an ollama provider without a key is usable: %+v", st)
		}
	}
}

func TestACustomAvailabilityFunctionDecidesPerProvider(t *testing.T) {
	cfg, _ := modelcfg.LoadBytes("models.yaml", []byte(baseConfig))
	p := routertest.NewProvider(nil)
	r, err := router.NewRouter(cfg, map[string]router.Provider{"ollama": p},
		router.WithClock(routertest.NewClock(t0)),
		router.WithAvailability(func(prov modelcfg.Provider) string {
			if prov.ID == "gpu-box" {
				return "custom_reason"
			}
			return ""
		}),
		router.WithLogger(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.Select([]string{"chat"})
	if ids(got) != "laptop-a,laptop-b" {
		t.Errorf("got %s", ids(got))
	}
}

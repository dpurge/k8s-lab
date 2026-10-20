package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"phraseforge/internal/config"
)

func defaultConfig(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "absent.yaml"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNewRouterBuildsOverTheExampleModelsFile(t *testing.T) {
	cfg := defaultConfig(t)
	t.Setenv(config.EnvModelsFile, "models.example.yaml")
	models, err := config.LoadModels(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := newRouter(models, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || len(models.Models) != 1 {
		t.Errorf("router = %v, models = %d", r, len(models.Models))
	}
	if status := r.Status(); len(status) != 1 || !status[0].Available {
		t.Errorf("status = %+v", status)
	}
}

func TestNewRouterStartsWithProvidersThatLackTheirCredentials(t *testing.T) {
	cfg := defaultConfig(t)
	path := filepath.Join(t.TempDir(), "models.yaml")
	const doc = `providers:
  - {id: claude, type: anthropic, api_key_from: env:PF_TEST_UNSET_ANTHROPIC_KEY}
  - {id: gcp, type: vertex, project: p, location: global, api_key_from: file:/distinctive-dir/vertex-token}
  - {id: local, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
models:
  - {id: opus, provider: claude, model: x, tags: [default], priority: 30}
  - {id: gemini, provider: gcp, model: x, tags: [default], priority: 20}
  - {id: small, provider: local, model: m, tags: [default], priority: 10, options: {num_ctx: 16384}}
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvModelsFile, path)
	t.Setenv("PF_TEST_UNSET_ANTHROPIC_KEY", "")
	models, err := config.LoadModels(cfg)
	if err != nil {
		t.Fatalf("a missing credential must not stop startup: %v", err)
	}
	r, err := newRouter(models, nil)
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for _, s := range r.Status() {
		reasons = append(reasons, s.ID+":"+s.Reason)
	}
	if got := strings.Join(reasons, " "); got != "opus:missing_credentials gemini:missing_credentials small:" {
		t.Errorf("status reasons = %s", got)
	}
	if got, err := r.Select([]string{"default"}); err != nil || len(got) != 1 || got[0].ID != "small" {
		t.Errorf("Select = %v, %v", got, err)
	}
}

func TestNewRouterStartsWithAHeaderBearingProviderAndSkipsItWhileAHeaderVariableIsUnset(t *testing.T) {
	cfg := defaultConfig(t)
	path := filepath.Join(t.TempDir(), "models.yaml")
	const doc = `providers:
  - {id: gateway, type: openai, base_url: "https://gw.test/v1", api_key_from: "", headers: {X-Title: app, X-Gw: "env:PF_TEST_UNSET_HEADER_VAR"}}
  - {id: local, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
models:
  - {id: gw, provider: gateway, model: x, tags: [default], priority: 20, options: {num_ctx: 16384}}
  - {id: small, provider: local, model: m, tags: [default], priority: 10, options: {num_ctx: 16384}}
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvModelsFile, path)
	t.Setenv("PF_TEST_UNSET_HEADER_VAR", "")
	models, err := config.LoadModels(cfg)
	if err != nil {
		t.Fatalf("a header-bearing provider must load: %v", err)
	}
	r, err := newRouter(models, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.Select([]string{"default"}); err != nil || len(got) != 1 || got[0].ID != "small" {
		t.Errorf("Select = %v, %v", got, err)
	}
	if status := r.Status(); status[0].Reason != "missing_credentials" || !status[1].Available {
		t.Errorf("status = %+v", status)
	}
	t.Setenv("PF_TEST_UNSET_HEADER_VAR", "a-value")
	if got, _ := r.Select([]string{"default"}); len(got) != 2 {
		t.Errorf("the variable appeared, Select = %v", got)
	}
}

func TestLoadModelsFailsFastWithoutAModelsFile(t *testing.T) {
	cfg := defaultConfig(t)
	t.Setenv(config.EnvModelsFile, filepath.Join(t.TempDir(), "absent.yaml"))
	if _, err := config.LoadModels(cfg); err == nil || !strings.Contains(err.Error(), config.EnvModelsFile) {
		t.Errorf("err = %v, want it to name %s", err, config.EnvModelsFile)
	}
}

func TestRetentionRulesFollowTheConfiguredDays(t *testing.T) {
	cfg := defaultConfig(t)
	const day = 24 * time.Hour
	got := map[string]time.Duration{}
	for _, r := range retentionRules(cfg) {
		got[r.Name] = r.Retention
	}
	want := map[string]time.Duration{"llm_calls": 30 * day, "jobs_done": 7 * day, "jobs_failed": 30 * day}
	if len(got) != len(want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
	for name, retention := range want {
		if got[name] != retention {
			t.Errorf("rule %s keeps %v, want %v", name, got[name], retention)
		}
	}
}

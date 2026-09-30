package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
}

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"PGHost", cfg.PGHost, "localhost"},
		{"PGPort", cfg.PGPort, "5432"},
		{"PGDatabase", cfg.PGDatabase, "knowledge"},
		{"QdrantURL", cfg.QdrantURL, "http://localhost:6333"},
		{"Providers[ollama].BaseURL", cfg.Providers["ollama"].BaseURL, "http://localhost:11434"},
		{"Providers[ollama].FirstTokenTimeoutSeconds", cfg.Providers["ollama"].FirstTokenTimeoutSeconds, 300},
		{"Providers[ollama].IdleTimeoutSeconds", cfg.Providers["ollama"].IdleTimeoutSeconds, 60},
		{"Providers[openrouter].BaseURL", cfg.Providers["openrouter"].BaseURL, "https://openrouter.ai/api/v1"},
		{"Providers[openai].BaseURL", cfg.Providers["openai"].BaseURL, "https://api.openai.com/v1"},
		{"Embeddings", cfg.Embeddings, EmbeddingsConfig{Provider: "ollama", Model: "bge-m3", Dimension: 1024}},
		{"Chat.Model", cfg.Chat.Model, "gemma4:12b"},
		{"Chat.NumCtx", cfg.Chat.NumCtx, 8192},
		{"GenerateTitle.Model", cfg.GenerateTitle.Model, "gemma4:12b"},
		{"GenerateSummary.Model", cfg.GenerateSummary.Model, "gemma4:12b"},
		{"Translate.Model", cfg.Translate.Model, "gemma4:12b"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v (default)", c.name, c.got, c.want)
		}
	}
	for name, p := range map[string]PurposeConfig{"Chat": cfg.Chat, "GenerateTitle": cfg.GenerateTitle, "GenerateSummary": cfg.GenerateSummary, "Translate": cfg.Translate} {
		if p.Provider != "ollama" || p.Think || p.TimeoutSeconds != 1800 || p.Prompt == "" {
			t.Errorf("%s = %+v, want provider ollama, think false, timeout 1800, a non-empty prompt", name, p)
		}
	}
	if !strings.Contains(cfg.Translate.Prompt, "{{language}}") {
		t.Errorf("Translate.Prompt = %q, want the {{language}} placeholder", cfg.Translate.Prompt)
	}
}

func TestLoadOverridesFromFile(t *testing.T) {
	writeConfig(t, ""+
		"providers:\n"+
		"  ollama:\n"+
		"    baseURL: http://ollama.lan:11434\n"+
		"    idleTimeoutSeconds: 30\n"+
		"chat:\n"+
		"  model: custom-model\n"+
		"  think: true\n"+
		"generateSummary:\n"+
		"  provider: openrouter\n"+
		"  model: some/model\n"+
		"  prompt: custom summary prompt\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Chat.Model != "custom-model" || !cfg.Chat.Think {
		t.Errorf("Chat = %+v, want model custom-model and think true (from file)", cfg.Chat)
	}
	if cfg.Chat.NumCtx != 8192 || cfg.Chat.Prompt == "" {
		t.Errorf("Chat = %+v, want numCtx and prompt kept from defaults (unset in file)", cfg.Chat)
	}
	if got := cfg.Providers["ollama"]; got.BaseURL != "http://ollama.lan:11434" || got.IdleTimeoutSeconds != 30 || got.FirstTokenTimeoutSeconds != 300 {
		t.Errorf("Providers[ollama] = %+v, want file baseURL/idle and default first-token", got)
	}
	if cfg.GenerateSummary.Provider != "openrouter" || cfg.GenerateSummary.Prompt != "custom summary prompt" {
		t.Errorf("GenerateSummary = %+v, want openrouter and the file prompt", cfg.GenerateSummary)
	}
	if cfg.GenerateTitle.Provider != "ollama" {
		t.Errorf("GenerateTitle.Provider = %q, want ollama (default, unset in file)", cfg.GenerateTitle.Provider)
	}
}

func TestLoadAPIKeysPerProviderFromEnv(t *testing.T) {
	writeConfig(t, "")
	t.Setenv("OLLAMA_API_KEY", "k-ollama")
	t.Setenv("OPENROUTER_API_KEY", "k-openrouter")
	t.Setenv("OPENAI_API_KEY", "k-openai")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"ollama": "k-ollama", "openrouter": "k-openrouter", "openai": "k-openai"} {
		if cfg.Providers[name].APIKey != want {
			t.Errorf("Providers[%s].APIKey = %q, want %q", name, cfg.Providers[name].APIKey, want)
		}
	}
}

func TestLLMConfigMapsPurposeAndProvider(t *testing.T) {
	writeConfig(t, "providers:\n  ollama:\n    baseURL: http://o:11434\n")
	t.Setenv("OLLAMA_API_KEY", "secret")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.LLM(cfg.Chat)
	if got.Provider != "ollama" || got.BaseURL != "http://o:11434" || got.APIKey != "secret" || got.Model != "gemma4:12b" || got.NumCtx != 8192 {
		t.Errorf("LLM(Chat) = %+v, want provider/baseURL/key/model/numCtx from config", got)
	}
	if got.Timeout != 1800*time.Second || got.FirstTokenTimeout != 300*time.Second || got.IdleTimeout != 60*time.Second {
		t.Errorf("LLM(Chat) timeouts = %v/%v/%v, want 1800s/300s/60s", got.Timeout, got.FirstTokenTimeout, got.IdleTimeout)
	}
}

func TestLoadRejectsRemovedKeys(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"chat baseURL", "chat:\n  baseURL: http://x:11434\n", "chat.baseURL is no longer supported; set providers.ollama.baseURL"},
		{"embeddings baseURL", "embeddings:\n  baseURL: http://x:11434\n", "embeddings.baseURL is no longer supported"},
		{"translate baseURL", "translate:\n  provider: openrouter\n  baseURL: http://x\n", "set providers.openrouter.baseURL"},
		{"generate section", "generate:\n  model: m\n", "use generateTitle and generateSummary"},
		{"prompts block", "prompts:\n  chat: hi\n", "move prompts.chat to chat.prompt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeConfig(t, c.yaml)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Load() error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

// TestLoadReportsAllRemovedKeysAtOnce: the lab's pre-registry configmap
// has several old keys; all must be listed, not just the first.
func TestLoadReportsAllRemovedKeysAtOnce(t *testing.T) {
	writeConfig(t, "chat:\n  baseURL: a\ngenerate:\n  baseURL: b\nprompts:\n  chat: c\n")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	for _, want := range []string{"chat.baseURL", "generate is no longer supported", "prompts is no longer supported"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestLoadRejectsRemovedEnvVars(t *testing.T) {
	writeConfig(t, "")
	t.Setenv("CHAT_API_KEY", "old")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CHAT_API_KEY is no longer supported") || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Errorf("Load() error = %v, want CHAT_API_KEY rejected with the replacement names", err)
	}
}

func TestLoadRejectsUnknownProvider(t *testing.T) {
	writeConfig(t, "translate:\n  provider: olama\nembeddings:\n  provider: nope\n")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), `translate.provider "olama" is not in providers`) || !strings.Contains(err.Error(), `embeddings.provider "nope"`) {
		t.Errorf("Load() error = %v, want both unknown providers reported", err)
	}
}

func TestLoadAcceptsFakeEmbeddings(t *testing.T) {
	writeConfig(t, "embeddings:\n  provider: fake\n")
	if _, err := Load(); err != nil {
		t.Errorf("Load() error = %v, want fake embeddings accepted without a registry entry", err)
	}
}

func TestLoadErrorsOnMalformedFile(t *testing.T) {
	writeConfig(t, "chat: [this is not valid: yaml structure")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for malformed YAML")
	}
}

func TestLoadNeverReadsCredentialsOrConnectionFieldsFromFile(t *testing.T) {
	// PGHost/PGPort/PGDatabase/QdrantURL are not file fields at all, so a
	// file that still sets them (stale ConfigMap) must be ignored in favor
	// of env/defaults, same as credentials always have been.
	writeConfig(t, "chat:\n  model: custom-model\n")
	t.Setenv("PGUSER", "from-env")
	t.Setenv("PGHOST", "from-env-host")
	t.Setenv("QDRANT_URL", "http://from-env:6333")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Chat.Model != "custom-model" {
		t.Errorf("Chat.Model = %q, want custom-model (still file-sourced)", cfg.Chat.Model)
	}
	if cfg.PGUser != "from-env" {
		t.Errorf("PGUser = %q, want from-env (credential, never file-sourced)", cfg.PGUser)
	}
	if cfg.PGHost != "from-env-host" {
		t.Errorf("PGHost = %q, want from-env-host (connection field, never file-sourced)", cfg.PGHost)
	}
	if cfg.QdrantURL != "http://from-env:6333" {
		t.Errorf("QdrantURL = %q, want http://from-env:6333 (connection field, never file-sourced)", cfg.QdrantURL)
	}
}

// TestConfigMapLoadsAndMatchesDefaults: the lab ConfigMap must load under
// the new shape (no removed keys) and keep today's behavior — the same
// models and prompts as the compiled-in defaults (trimmed: the chat
// prompt's "|" block adds a trailing newline, which chat.go trims too).
func TestConfigMapLoadsAndMatchesDefaults(t *testing.T) {
	raw, err := os.ReadFile("../../k8s/configmap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, cm.Data["config.yaml"])
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(configmap) error = %v", err)
	}
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "missing.yaml"))
	def, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]PurposeConfig{
		"chat": {cfg.Chat, def.Chat}, "generateTitle": {cfg.GenerateTitle, def.GenerateTitle},
		"generateSummary": {cfg.GenerateSummary, def.GenerateSummary}, "translate": {cfg.Translate, def.Translate},
	} {
		got, want := pair[0], pair[1]
		if strings.TrimSpace(got.Prompt) != strings.TrimSpace(want.Prompt) {
			t.Errorf("%s prompt differs from default:\n%q\nvs\n%q", name, got.Prompt, want.Prompt)
		}
		if got.Model != want.Model || got.NumCtx != want.NumCtx || got.Provider != want.Provider || got.TimeoutSeconds != want.TimeoutSeconds {
			t.Errorf("%s = %+v, want model/numCtx/provider/timeout of default %+v", name, got, want)
		}
	}
	if cfg.Providers["ollama"].BaseURL != "http://host.docker.internal:11434" {
		t.Errorf("Providers[ollama].BaseURL = %q, want the lab's host.docker.internal", cfg.Providers["ollama"].BaseURL)
	}
}

func TestEmbeddingsConnectionMapsProviderToAPIStyle(t *testing.T) {
	cfg := Config{Providers: map[string]ProviderConfig{
		"ollama":     {BaseURL: "http://o"},
		"openrouter": {BaseURL: "https://or", APIKey: "k"},
	}}
	for provider, want := range map[string][3]string{
		"ollama":     {"ollama", "http://o", ""},
		"openrouter": {"openai", "https://or", "k"},
		"fake":       {"fake", "", ""},
	} {
		cfg.Embeddings.Provider = provider
		style, baseURL, key := cfg.EmbeddingsConnection()
		if got := [3]string{style, baseURL, key}; got != want {
			t.Errorf("EmbeddingsConnection() for %s = %v, want %v", provider, got, want)
		}
	}
}

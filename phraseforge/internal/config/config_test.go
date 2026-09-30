package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"BindAddr", cfg.BindAddr, "0.0.0.0:8090"},
		{"PGHost", cfg.PGHost, "localhost"},
		{"PGPort", cfg.PGPort, "5432"},
		{"PGDatabase", cfg.PGDatabase, "phraseforge"},
		{"Providers[ollama].BaseURL", cfg.Providers["ollama"].BaseURL, "http://host.docker.internal:11434"},
		{"Providers[openrouter].BaseURL", cfg.Providers["openrouter"].BaseURL, "https://openrouter.ai/api/v1"},
		{"Transcription.Provider", cfg.Transcription.Provider, "ollama"},
		{"Transcription.Model", cfg.Transcription.Model, "gemma4:12b"},
		{"Translation.Provider", cfg.Translation.Provider, "ollama"},
		{"Translation.Model", cfg.Translation.Model, "gemma4:12b"},
		{"Title.Provider", cfg.Title.Provider, "ollama"},
		{"Title.Model", cfg.Title.Model, "gemma4:12b"},
		{"ProcessText.Provider", cfg.ProcessText.Provider, "ollama"},
		{"ProcessText.Model", cfg.ProcessText.Model, "gemma4:12b"},
		{"ProcessDialog.Provider", cfg.ProcessDialog.Provider, "ollama"},
		{"ProcessDialog.Model", cfg.ProcessDialog.Model, "gemma4:12b"},
		{"SessionKey", cfg.SessionKey, "dev-only-insecure-key-change-me"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (default)", c.name, c.got, c.want)
		}
	}
	// Title/ProcessText/ProcessDialog default to NumCtx 8192, unlike
	// Transcription/Translation which are left unset (0) — see
	// specs/features/phraseforge-ingest-texts-dialogs.md.
	numCtxCases := []struct {
		name string
		got  int
		want int
	}{
		{"Transcription.NumCtx", cfg.Transcription.NumCtx, 0},
		{"Translation.NumCtx", cfg.Translation.NumCtx, 0},
		{"Title.NumCtx", cfg.Title.NumCtx, 8192},
		{"ProcessText.NumCtx", cfg.ProcessText.NumCtx, 8192},
		{"ProcessDialog.NumCtx", cfg.ProcessDialog.NumCtx, 8192},
	}
	for _, c := range numCtxCases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (default)", c.name, c.got, c.want)
		}
	}
	// TimeoutSeconds: 1800s overall backstop for every purpose; streamed
	// calls are bounded first by the provider's first-token/idle limits
	// (see llm-streaming-progress-timeout).
	timeoutCases := []struct {
		name string
		got  int
		want int
	}{
		{"Transcription.TimeoutSeconds", cfg.Transcription.TimeoutSeconds, 1800},
		{"Translation.TimeoutSeconds", cfg.Translation.TimeoutSeconds, 1800},
		{"Title.TimeoutSeconds", cfg.Title.TimeoutSeconds, 1800},
		{"ProcessText.TimeoutSeconds", cfg.ProcessText.TimeoutSeconds, 1800},
		{"ProcessDialog.TimeoutSeconds", cfg.ProcessDialog.TimeoutSeconds, 1800},
		{"GenerateVocabulary.TimeoutSeconds", cfg.GenerateVocabulary.TimeoutSeconds, 1800},
		{"GenerateModels.TimeoutSeconds", cfg.GenerateModels.TimeoutSeconds, 1800},
		{"VocabularyItem.TimeoutSeconds", cfg.VocabularyItem.TimeoutSeconds, 1800},
		{"ModelsItem.TimeoutSeconds", cfg.ModelsItem.TimeoutSeconds, 1800},
	}
	for _, c := range timeoutCases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (default)", c.name, c.got, c.want)
		}
	}
	if got := cfg.Providers["ollama"].FirstTokenTimeoutSeconds; got != 300 {
		t.Errorf("Providers[ollama].FirstTokenTimeoutSeconds = %d, want 300 (default)", got)
	}
	if got := cfg.Providers["ollama"].IdleTimeoutSeconds; got != 60 {
		t.Errorf("Providers[ollama].IdleTimeoutSeconds = %d, want 60 (default)", got)
	}
	// Prompt must be non-empty for every purpose — a blank default would
	// silently defeat ai.go's fallback-to-config-default behavior.
	promptCases := []struct {
		name string
		got  string
	}{
		{"Transcription.Prompt", cfg.Transcription.Prompt},
		{"Translation.Prompt", cfg.Translation.Prompt},
		{"VocabularyItem.Prompt", cfg.VocabularyItem.Prompt},
		{"ModelsItem.Prompt", cfg.ModelsItem.Prompt},
		{"Title.Prompt", cfg.Title.Prompt},
		{"ProcessText.Prompt", cfg.ProcessText.Prompt},
		{"ProcessDialog.Prompt", cfg.ProcessDialog.Prompt},
		{"GenerateVocabulary.Prompt", cfg.GenerateVocabulary.Prompt},
		{"GenerateModels.Prompt", cfg.GenerateModels.Prompt},
	}
	for _, c := range promptCases {
		if c.got == "" {
			t.Errorf("%s = \"\", want a non-empty default prompt", c.name)
		}
	}
}

func TestLoadOverridesFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "" +
		"bindAddr: \"0.0.0.0:9090\"\n" +
		"providers:\n" +
		"  ollama:\n" +
		"    baseURL: http://custom-ollama:11434\n" +
		"    firstTokenTimeoutSeconds: 90\n" +
		"transcription:\n" +
		"  model: custom-model\n" +
		"  timeoutSeconds: 45\n" +
		"  prompt: custom transcription prompt\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.BindAddr != "0.0.0.0:9090" {
		t.Errorf("BindAddr = %q, want 0.0.0.0:9090 (from file)", cfg.BindAddr)
	}
	if cfg.PGDatabase != "phraseforge" {
		t.Errorf("PGDatabase = %q, want phraseforge (default, no longer file-sourced)", cfg.PGDatabase)
	}
	if cfg.Providers["ollama"].BaseURL != "http://custom-ollama:11434" {
		t.Errorf("Providers[ollama].BaseURL = %q, want http://custom-ollama:11434 (from file)", cfg.Providers["ollama"].BaseURL)
	}
	if cfg.Transcription.Model != "custom-model" {
		t.Errorf("Transcription.Model = %q, want custom-model (from file)", cfg.Transcription.Model)
	}
	if cfg.Transcription.TimeoutSeconds != 45 {
		t.Errorf("Transcription.TimeoutSeconds = %d, want 45 (from file)", cfg.Transcription.TimeoutSeconds)
	}
	if cfg.Transcription.Prompt != "custom transcription prompt" {
		t.Errorf("Transcription.Prompt = %q, want %q (from file)", cfg.Transcription.Prompt, "custom transcription prompt")
	}
	// Fields left unset in the file must keep their Go defaults.
	if cfg.Translation.Model != "gemma4:12b" {
		t.Errorf("Translation.Model = %q, want gemma4:12b (default, unset in file)", cfg.Translation.Model)
	}
	if cfg.Translation.TimeoutSeconds != 1800 {
		t.Errorf("Translation.TimeoutSeconds = %d, want 1800 (default, unset in file)", cfg.Translation.TimeoutSeconds)
	}
	if got := cfg.Providers["ollama"].FirstTokenTimeoutSeconds; got != 90 {
		t.Errorf("Providers[ollama].FirstTokenTimeoutSeconds = %d, want 90 (from file)", got)
	}
	if got := cfg.Providers["ollama"].IdleTimeoutSeconds; got != 60 {
		t.Errorf("Providers[ollama].IdleTimeoutSeconds = %d, want 60 (default, unset in file)", got)
	}
	if cfg.Providers["openrouter"].BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("Providers[openrouter].BaseURL = %q, want https://openrouter.ai/api/v1 (default, unset in file)", cfg.Providers["openrouter"].BaseURL)
	}
}

func TestLoadEnvOverridesCredentialsAndSessionKey(t *testing.T) {
	// No file at all — env vars must still apply on top of pure defaults.
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	t.Setenv("PGUSER", "env-user")
	t.Setenv("PGPASSWORD", "env-password")
	t.Setenv("SESSION_KEY", "env-session-key")
	t.Setenv("OLLAMA_API_KEY", "env-ollama-key")
	t.Setenv("OPENROUTER_API_KEY", "env-openrouter-key")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"PGUser", cfg.PGUser, "env-user"},
		{"PGPassword", cfg.PGPassword, "env-password"},
		{"SessionKey", cfg.SessionKey, "env-session-key"},
		{"Providers[ollama].APIKey", cfg.Providers["ollama"].APIKey, "env-ollama-key"},
		{"Providers[openrouter].APIKey", cfg.Providers["openrouter"].APIKey, "env-openrouter-key"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (from env)", c.name, c.got, c.want)
		}
	}
}

func TestLoadEnvOverridesConnectionFields(t *testing.T) {
	// No file at all — env vars must still apply on top of pure defaults.
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	t.Setenv("PGHOST", "env-host")
	t.Setenv("PGPORT", "9999")
	t.Setenv("PGDATABASE", "env-db")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"PGHost", cfg.PGHost, "env-host"},
		{"PGPort", cfg.PGPort, "9999"},
		{"PGDatabase", cfg.PGDatabase, "env-db"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (from env)", c.name, c.got, c.want)
		}
	}
}

func TestLoadEnvOverridesOnTopOfFile(t *testing.T) {
	// Credentials and Postgres connection fields are never read from the
	// mounted file (see fileConfig's doc comment) — env must win even when a
	// file is present and sets unrelated, still-file-sourced fields.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  ollama:\n    baseURL: http://from-file:11434\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("PGUSER", "from-env")
	t.Setenv("PGHOST", "from-env-host")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Providers["ollama"].BaseURL != "http://from-file:11434" {
		t.Errorf("Providers[ollama].BaseURL = %q, want http://from-file:11434 (still file-sourced)", cfg.Providers["ollama"].BaseURL)
	}
	if cfg.PGUser != "from-env" {
		t.Errorf("PGUser = %q, want from-env (credential, never file-sourced)", cfg.PGUser)
	}
	if cfg.PGHost != "from-env-host" {
		t.Errorf("PGHost = %q, want from-env-host (connection field, never file-sourced)", cfg.PGHost)
	}
}

func TestLoadMissingFileIsNotError(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if _, err := Load(); err != nil {
		t.Errorf("Load() error = %v, want nil for a missing config file", err)
	}
}

func TestLoadErrorsOnMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("bindAddr: [this is not valid: yaml structure"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for malformed YAML")
	}
}

// TestConfigMapItemPromptsMatchDefaults: k8s/configmap.yaml declares every
// purpose explicitly, so its item-prompt templates must stay byte-identical
// to the compiled-in defaults (the ones mirrored from prompt-eval).
func TestConfigMapItemPromptsMatchDefaults(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(cm.Data["config.yaml"]), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VocabularyItem.Prompt != DefaultVocabularyItemPrompt {
		t.Errorf("configmap vocabularyItem.prompt differs from DefaultVocabularyItemPrompt:\n%q\nvs\n%q", cfg.VocabularyItem.Prompt, DefaultVocabularyItemPrompt)
	}
	if cfg.ModelsItem.Prompt != DefaultModelsItemPrompt {
		t.Errorf("configmap modelsItem.prompt differs from DefaultModelsItemPrompt:\n%q\nvs\n%q", cfg.ModelsItem.Prompt, DefaultModelsItemPrompt)
	}
}

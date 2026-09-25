package config

import (
	"os"
	"path/filepath"
	"testing"
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
}

func TestLoadOverridesFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "" +
		"bindAddr: \"0.0.0.0:9090\"\n" +
		"postgres:\n" +
		"  database: custom_db\n" +
		"providers:\n" +
		"  ollama:\n" +
		"    baseURL: http://custom-ollama:11434\n" +
		"transcription:\n" +
		"  model: custom-model\n"
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
	if cfg.PGDatabase != "custom_db" {
		t.Errorf("PGDatabase = %q, want custom_db (from file)", cfg.PGDatabase)
	}
	if cfg.Providers["ollama"].BaseURL != "http://custom-ollama:11434" {
		t.Errorf("Providers[ollama].BaseURL = %q, want http://custom-ollama:11434 (from file)", cfg.Providers["ollama"].BaseURL)
	}
	if cfg.Transcription.Model != "custom-model" {
		t.Errorf("Transcription.Model = %q, want custom-model (from file)", cfg.Transcription.Model)
	}
	// Fields left unset in the file must keep their Go defaults.
	if cfg.Translation.Model != "gemma4:12b" {
		t.Errorf("Translation.Model = %q, want gemma4:12b (default, unset in file)", cfg.Translation.Model)
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

func TestLoadEnvOverridesOnTopOfFile(t *testing.T) {
	// Credentials are never read from the mounted file (see fileConfig's
	// doc comment) — env must win even when a file is present and sets
	// unrelated, non-credential fields.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("postgres:\n  host: from-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("PGUSER", "from-env")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.PGHost != "from-file" {
		t.Errorf("PGHost = %q, want from-file (non-credential, file-sourced)", cfg.PGHost)
	}
	if cfg.PGUser != "from-env" {
		t.Errorf("PGUser = %q, want from-env (credential, never file-sourced)", cfg.PGUser)
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

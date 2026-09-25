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
	if cfg.ChatModel != "gemma4:12b" {
		t.Errorf("ChatModel = %q, want gemma4:12b (default)", cfg.ChatModel)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"PGHost", cfg.PGHost, "localhost"},
		{"PGPort", cfg.PGPort, "5432"},
		{"PGDatabase", cfg.PGDatabase, "knowledge"},
		{"QdrantURL", cfg.QdrantURL, "http://localhost:6333"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (default)", c.name, c.got, c.want)
		}
	}
}

func TestLoadOverridesFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("chat:\n  model: custom-model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.ChatModel != "custom-model" {
		t.Errorf("ChatModel = %q, want custom-model (from file)", cfg.ChatModel)
	}
	if cfg.GenerateModel != "gemma4:12b" {
		t.Errorf("GenerateModel = %q, want gemma4:12b (default, unset in file)", cfg.GenerateModel)
	}
}

func TestLoadErrorsOnMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("chat: [this is not valid: yaml structure"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for malformed YAML")
	}
}

func TestLoadNeverReadsCredentialsOrConnectionFieldsFromFile(t *testing.T) {
	// PGHost/PGPort/PGDatabase/QdrantURL are no longer file fields at all,
	// so a file that still sets them (stale ConfigMap) must be ignored in
	// favor of env/defaults, same as credentials always have been.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("chat:\n  model: custom-model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("PGUSER", "from-env")
	t.Setenv("PGHOST", "from-env-host")
	t.Setenv("QDRANT_URL", "http://from-env:6333")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.ChatModel != "custom-model" {
		t.Errorf("ChatModel = %q, want custom-model (still file-sourced)", cfg.ChatModel)
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

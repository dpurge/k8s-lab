package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds everything the server needs to start.
type Config struct {
	BindAddr string

	PGHost     string
	PGPort     string
	PGDatabase string
	PGUser     string
	PGPassword string

	// Providers maps a provider key (e.g. "ollama", "openrouter") to its
	// connection details. Transcription/Translation.Provider and the
	// llm_prompts.provider column both reference these keys by name.
	Providers map[string]ProviderConfig

	Transcription      PurposeConfig
	Translation        PurposeConfig
	Title              PurposeConfig
	ProcessText        PurposeConfig
	ProcessDialog      PurposeConfig
	GenerateVocabulary PurposeConfig
	GenerateModels     PurposeConfig

	// SessionKey signs the login session cookie. Must be set in production;
	// a fixed dev default is used only so `go run` works with zero setup.
	SessionKey string
}

// ProviderConfig holds one LLM provider's connection details. APIKey is
// never read from the mounted file — only from that provider's own env var
// (OLLAMA_API_KEY, OPENROUTER_API_KEY) — and never stored in the database.
type ProviderConfig struct {
	BaseURL string
	APIKey  string
}

// PurposeConfig is a per-purpose (transcription/translation) LLM default,
// used when no admin-configured llm_prompts row exists for a given
// (kind, source_language, target_language).
type PurposeConfig struct {
	Provider string
	Model    string
	NumCtx   int
	Think    bool
}

// fileConfig mirrors the mounted ConfigMap YAML file's shape. Credentials
// (PGUser/PGPassword, the provider API keys) are deliberately absent here —
// they stay plain/Secret-sourced env vars, never read from this file.
type fileConfig struct {
	BindAddr string `yaml:"bindAddr"`
	Postgres struct {
		Host     string `yaml:"host"`
		Port     string `yaml:"port"`
		Database string `yaml:"database"`
	} `yaml:"postgres"`
	Providers struct {
		Ollama struct {
			BaseURL string `yaml:"baseURL"`
		} `yaml:"ollama"`
		OpenRouter struct {
			BaseURL string `yaml:"baseURL"`
		} `yaml:"openrouter"`
	} `yaml:"providers"`
	Transcription      purposeFileConfig `yaml:"transcription"`
	Translation        purposeFileConfig `yaml:"translation"`
	Title              purposeFileConfig `yaml:"title"`
	ProcessText        purposeFileConfig `yaml:"processText"`
	ProcessDialog      purposeFileConfig `yaml:"processDialog"`
	GenerateVocabulary purposeFileConfig `yaml:"generateVocabulary"`
	GenerateModels     purposeFileConfig `yaml:"generateModels"`
}

type purposeFileConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	NumCtx   int    `yaml:"numCtx"`
	Think    bool   `yaml:"think"`
}

func defaultFileConfig() fileConfig {
	var f fileConfig
	f.BindAddr = "0.0.0.0:8090"
	f.Postgres.Host = "localhost"
	f.Postgres.Port = "5432"
	f.Postgres.Database = "phraseforge"
	f.Providers.Ollama.BaseURL = "http://host.docker.internal:11434"
	f.Providers.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	f.Transcription = purposeFileConfig{Provider: "ollama", Model: "gemma4:12b", Think: false}
	f.Translation = purposeFileConfig{Provider: "ollama", Model: "gemma4:12b", Think: false}
	f.Title = purposeFileConfig{Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false}
	f.ProcessText = purposeFileConfig{Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false}
	f.ProcessDialog = purposeFileConfig{Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false}
	f.GenerateVocabulary = purposeFileConfig{Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false}
	f.GenerateModels = purposeFileConfig{Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false}
	return f
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Load reads the mounted config file (path from CONFIG_FILE, default
// /etc/phraseforge/config.yaml) over built-in defaults — a missing file is
// fine (defaults apply, so local runs without a cluster still work), but a
// malformed one is a real error, not something to silently paper over.
func Load() (Config, error) {
	fc := defaultFileConfig()
	path := env("CONFIG_FILE", "/etc/phraseforge/config.yaml")
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// use defaults
	case err != nil:
		return Config{}, fmt.Errorf("read config file %s: %w", path, err)
	default:
		if err := yaml.Unmarshal(data, &fc); err != nil {
			return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
		}
	}

	return Config{
		BindAddr: fc.BindAddr,

		PGHost:     fc.Postgres.Host,
		PGPort:     fc.Postgres.Port,
		PGDatabase: fc.Postgres.Database,
		PGUser:     env("PGUSER", "phraseforge"),
		PGPassword: env("PGPASSWORD", ""),

		Providers: map[string]ProviderConfig{
			"ollama":     {BaseURL: fc.Providers.Ollama.BaseURL, APIKey: env("OLLAMA_API_KEY", "")},
			"openrouter": {BaseURL: fc.Providers.OpenRouter.BaseURL, APIKey: env("OPENROUTER_API_KEY", "")},
		},

		Transcription:      PurposeConfig(fc.Transcription),
		Translation:        PurposeConfig(fc.Translation),
		Title:              PurposeConfig(fc.Title),
		ProcessText:        PurposeConfig(fc.ProcessText),
		ProcessDialog:      PurposeConfig(fc.ProcessDialog),
		GenerateVocabulary: PurposeConfig(fc.GenerateVocabulary),
		GenerateModels:     PurposeConfig(fc.GenerateModels),

		SessionKey: env("SESSION_KEY", "dev-only-insecure-key-change-me"),
	}, nil
}

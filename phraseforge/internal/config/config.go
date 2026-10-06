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
	// VocabularyItem and ModelsItem translate one list item into one site
	// locale in a single structured (JSON) call; their prompts are
	// templates with the prompt-eval setup's camelCase placeholders (see
	// specs/features/phraseforge-structured-item-translation.md).
	VocabularyItem PurposeConfig
	ModelsItem     PurposeConfig

	// IngestMaxContentBytes caps the raw content one ingest request may carry
	// (yaml ingest.maxContentBytes). Raising it above what one LLM call can
	// hold relies on the content being processed in chunks.
	IngestMaxContentBytes int

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
	// FirstTokenTimeoutSeconds and IdleTimeoutSeconds bound a streamed
	// call's wait for its first output (model load + prompt eval) and the
	// gap between outputs afterwards. They belong to the provider, not the
	// purpose, because they reflect how fast that host runs a model. 0
	// means shared/llm's own default. Only the Ollama path streams.
	FirstTokenTimeoutSeconds int
	IdleTimeoutSeconds       int
}

// PurposeConfig is a per-purpose (transcription/translation) LLM default,
// used when no admin-configured llm_prompts row exists for a given
// (kind, source_language, target_language).
type PurposeConfig struct {
	Provider string
	Model    string
	NumCtx   int
	Think    bool
	// TimeoutSeconds is this purpose's default LLM call timeout — 0 means
	// "no purpose-level override", falling through to shared/llm's own
	// built-in default. An admin llm_prompts.timeout_seconds override (see
	// ai.go's resolveTimeout) takes precedence over this when set.
	TimeoutSeconds int
	// Prompt is this purpose's default system prompt — used when no
	// admin-configured llm_prompts row exists, or its own prompt is blank.
	// Previously hardcoded in ai.go's prompt() switch; moved here so it's
	// inspectable/editable without a rebuild (see
	// specs/features/llm-purpose-timeout-and-prompt-config.md).
	Prompt string
	// MaxAttempts is the total attempts (first try included) one LLM call
	// gets when its reply fails validation and is sent back to the model for
	// correction; <= 0 means the built-in default of 3 (see ai/retry.go).
	MaxAttempts int
}

// fileConfig mirrors the mounted ConfigMap YAML file's shape. Credentials
// (PGUser/PGPassword, the provider API keys) and Postgres connection fields
// (PGHost/PGPort/PGDatabase) are deliberately absent here — they stay
// plain/Secret-sourced env vars, never read from this file, so `migrate`
// works before the ConfigMap exists (it's a pre-install/pre-upgrade hook).
type fileConfig struct {
	BindAddr  string `yaml:"bindAddr"`
	Providers struct {
		Ollama struct {
			BaseURL                  string `yaml:"baseURL"`
			FirstTokenTimeoutSeconds int    `yaml:"firstTokenTimeoutSeconds"`
			IdleTimeoutSeconds       int    `yaml:"idleTimeoutSeconds"`
		} `yaml:"ollama"`
		OpenRouter struct {
			BaseURL string `yaml:"baseURL"`
		} `yaml:"openrouter"`
	} `yaml:"providers"`
	Ingest struct {
		MaxContentBytes int `yaml:"maxContentBytes"`
	} `yaml:"ingest"`
	Transcription      purposeFileConfig `yaml:"transcription"`
	Translation        purposeFileConfig `yaml:"translation"`
	Title              purposeFileConfig `yaml:"title"`
	ProcessText        purposeFileConfig `yaml:"processText"`
	ProcessDialog      purposeFileConfig `yaml:"processDialog"`
	GenerateVocabulary purposeFileConfig `yaml:"generateVocabulary"`
	GenerateModels     purposeFileConfig `yaml:"generateModels"`
	VocabularyItem     purposeFileConfig `yaml:"vocabularyItem"`
	ModelsItem         purposeFileConfig `yaml:"modelsItem"`
}

type purposeFileConfig struct {
	Provider       string `yaml:"provider"`
	Model          string `yaml:"model"`
	NumCtx         int    `yaml:"numCtx"`
	Think          bool   `yaml:"think"`
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
	Prompt         string `yaml:"prompt"`
	MaxAttempts    int    `yaml:"maxAttempts"`
}

func defaultFileConfig() fileConfig {
	var f fileConfig
	f.BindAddr = "0.0.0.0:8090"
	f.Providers.Ollama.BaseURL = "http://host.docker.internal:11434"
	f.Providers.Ollama.FirstTokenTimeoutSeconds = 300
	f.Providers.Ollama.IdleTimeoutSeconds = 60
	f.Providers.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	f.Ingest.MaxContentBytes = 24 * 1024
	// TimeoutSeconds is only an overall backstop (1800s for every purpose):
	// streamed Ollama calls are primarily bounded by the provider's
	// first-token/idle limits, since a CPU-only node legitimately needs
	// ~300s for one short translation (see
	// specs/features/llm-streaming-progress-timeout.md). Prompt text is
	// copied verbatim from ai.go's former hardcoded switch so upgrading
	// doesn't change any existing behavior.
	f.Transcription = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultTranscriptionPrompt,
	}
	f.Translation = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultTranslationPrompt,
	}
	f.Title = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultTitlePrompt,
	}
	f.ProcessText = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultProcessTextPrompt,
	}
	f.ProcessDialog = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultProcessDialogPrompt,
	}
	f.GenerateVocabulary = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultGenerateVocabularyPrompt,
	}
	f.GenerateModels = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultGenerateModelsPrompt,
	}
	// NumCtx left 0 (Ollama's default) to match the prompt-eval request.
	f.VocabularyItem = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultVocabularyItemPrompt,
	}
	f.ModelsItem = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 1800, MaxAttempts: 3,
		Prompt: DefaultModelsItemPrompt,
	}
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
	if fc.Ingest.MaxContentBytes <= 0 {
		return Config{}, fmt.Errorf("config file %s: ingest.maxContentBytes must be greater than 0, got %d", path, fc.Ingest.MaxContentBytes)
	}

	return Config{
		BindAddr: fc.BindAddr,

		PGHost:     env("PGHOST", "localhost"),
		PGPort:     env("PGPORT", "5432"),
		PGDatabase: env("PGDATABASE", "phraseforge"),
		PGUser:     env("PGUSER", "phraseforge"),
		PGPassword: env("PGPASSWORD", ""),

		Providers: map[string]ProviderConfig{
			"ollama": {
				BaseURL: fc.Providers.Ollama.BaseURL, APIKey: env("OLLAMA_API_KEY", ""),
				FirstTokenTimeoutSeconds: fc.Providers.Ollama.FirstTokenTimeoutSeconds,
				IdleTimeoutSeconds:       fc.Providers.Ollama.IdleTimeoutSeconds,
			},
			"openrouter": {BaseURL: fc.Providers.OpenRouter.BaseURL, APIKey: env("OPENROUTER_API_KEY", "")},
		},

		Transcription:      PurposeConfig(fc.Transcription),
		Translation:        PurposeConfig(fc.Translation),
		Title:              PurposeConfig(fc.Title),
		ProcessText:        PurposeConfig(fc.ProcessText),
		ProcessDialog:      PurposeConfig(fc.ProcessDialog),
		GenerateVocabulary: PurposeConfig(fc.GenerateVocabulary),
		GenerateModels:     PurposeConfig(fc.GenerateModels),
		VocabularyItem:     PurposeConfig(fc.VocabularyItem),
		ModelsItem:         PurposeConfig(fc.ModelsItem),

		IngestMaxContentBytes: fc.Ingest.MaxContentBytes,

		SessionKey: env("SESSION_KEY", "dev-only-insecure-key-change-me"),
	}, nil
}

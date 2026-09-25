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
	Provider       string `yaml:"provider"`
	Model          string `yaml:"model"`
	NumCtx         int    `yaml:"numCtx"`
	Think          bool   `yaml:"think"`
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
	Prompt         string `yaml:"prompt"`
}

func defaultFileConfig() fileConfig {
	var f fileConfig
	f.BindAddr = "0.0.0.0:8090"
	f.Providers.Ollama.BaseURL = "http://host.docker.internal:11434"
	f.Providers.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	// TimeoutSeconds: 120s for transcription/translation/title, 300s for
	// process_text/process_dialog, 600s for generate_vocabulary/
	// generate_models — the latter two are the ones observed exceeding the
	// old fixed 2-minute client timeout (see this feature's Problem/
	// Motivation). Prompt text is copied verbatim from ai.go's former
	// hardcoded switch so upgrading doesn't change any existing behavior.
	f.Transcription = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 120,
		Prompt: "Create a romanized transcription for the source language content. Return only the transcription, preserving line breaks and structure. Do not add explanations.",
	}
	f.Translation = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", Think: false, TimeoutSeconds: 120,
		Prompt: "Translate the source language content to the target language. Return only the translation, preserving line breaks and structure. Do not add explanations.",
	}
	f.Title = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 120,
		Prompt: "You write a short, specific title for the given text. Respond with only the title text on a single line — no quotes, no punctuation at the end, no preamble.",
	}
	f.ProcessText = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 300,
		Prompt: "You reformat raw extracted text into clean Markdown prose suitable as a language-learning reading text. Remove navigation menus, ads, boilerplate, and unrelated content. Preserve the actual article/passage content and its paragraph structure. Do not translate or summarize. Respond with only the cleaned Markdown.",
	}
	f.ProcessDialog = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 300,
		Prompt: "You reformat raw extracted text into a clean dialog transcript in Markdown. Identify distinct speakers/turns and format each turn on its own line. Remove navigation, ads, and unrelated content. Respond with only the cleaned dialog content.",
	}
	f.GenerateVocabulary = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 600,
		Prompt: "You extract vocabulary and grammar items from the given text for a language learner. Respond ONLY with one item per line, in this exact format: phrase {grammar} [transcription] = translation — where {grammar} is a short grammar tag (e.g. part of speech), [transcription] is a romanized reading, and = translation is the item's translation; each of {grammar}, [transcription], and = translation is optional and must be omitted entirely (not left as empty brackets) when not applicable. Do not add commentary, a preamble, numbering, or code fences — only the item lines themselves.",
	}
	f.GenerateModels = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, Think: false, TimeoutSeconds: 600,
		Prompt: "You extract short grammar/sentence-pattern models from the given text for a language learner. Respond ONLY with one item per line, in this exact format: phrase [transcription] = translation — where [transcription] is a romanized reading and = translation is the item's translation; each of [transcription] and = translation is optional and must be omitted entirely (not left as empty brackets) when not applicable. Do not add commentary, a preamble, numbering, or code fences — only the item lines themselves.",
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

	return Config{
		BindAddr: fc.BindAddr,

		PGHost:     env("PGHOST", "localhost"),
		PGPort:     env("PGPORT", "5432"),
		PGDatabase: env("PGDATABASE", "phraseforge"),
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

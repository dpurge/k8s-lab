package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"k8s-lab/shared/llm"
)

type Config struct {
	BindAddr string

	QdrantURL        string
	QdrantCollection string
	SearchMinScore   float64

	PGHost     string
	PGPort     string
	PGDatabase string
	PGUser     string
	PGPassword string

	// Providers maps a provider key ("ollama", "openrouter", "openai") to
	// its connection details; every purpose (and Embeddings) names one of
	// these keys — phraseforge's config shape.
	Providers map[string]ProviderConfig

	Embeddings EmbeddingsConfig

	Chat            PurposeConfig
	GenerateTitle   PurposeConfig
	GenerateSummary PurposeConfig
	Translate       PurposeConfig

	KnowledgeLanguage string
}

// ProviderConfig is one LLM provider's connection details. APIKey is never
// read from the mounted file — only from that provider's own env var
// (OLLAMA_API_KEY, OPENROUTER_API_KEY, OPENAI_API_KEY).
type ProviderConfig struct {
	BaseURL string
	APIKey  string
	// FirstTokenTimeoutSeconds and IdleTimeoutSeconds bound a streamed
	// call's wait for its first output (model load + prompt eval) and the
	// gap between outputs afterwards; 0 means shared/llm's own default.
	// Only the Ollama path streams.
	FirstTokenTimeoutSeconds int
	IdleTimeoutSeconds       int
}

// PurposeConfig is one LLM use: which provider and model, its runtime
// knobs, and its system prompt.
type PurposeConfig struct {
	Provider string
	Model    string
	// NumCtx sets Ollama's options.num_ctx; 0 leaves Ollama's default.
	NumCtx int
	Think  bool
	// TimeoutSeconds caps the whole call — a backstop; the provider's
	// first-token/idle limits are what catch a hung call.
	TimeoutSeconds int
	Prompt         string
}

// EmbeddingsConfig selects the embedding model. Provider is a registry key
// or "fake" (no provider entry needed).
type EmbeddingsConfig struct {
	Provider  string
	Model     string
	Dimension int
}

// FakeEmbeddingsProvider needs no registry entry (tests/local runs).
const FakeEmbeddingsProvider = "fake"

// LLM builds the shared/llm client config for p. Safe for any purpose Load
// returned, since Load rejects a purpose whose provider isn't registered.
func (c Config) LLM(p PurposeConfig) llm.Config {
	prov := c.Providers[p.Provider]
	return llm.Config{
		Provider: p.Provider,
		BaseURL:  prov.BaseURL,
		APIKey:   prov.APIKey,
		Model:    p.Model,
		NumCtx:   p.NumCtx,
		Think:    p.Think,
		Timeout:  time.Duration(p.TimeoutSeconds) * time.Second,

		FirstTokenTimeout: time.Duration(prov.FirstTokenTimeoutSeconds) * time.Second,
		IdleTimeout:       time.Duration(prov.IdleTimeoutSeconds) * time.Second,
	}
}

// EmbeddingsConnection resolves Embeddings.Provider to what
// embeddings.New takes: the API style ("ollama", "openai" for any
// OpenAI-compatible provider such as openrouter, or "fake"), plus the
// provider's base URL and API key.
func (c Config) EmbeddingsConnection() (apiStyle, baseURL, apiKey string) {
	switch c.Embeddings.Provider {
	case FakeEmbeddingsProvider, "ollama":
		apiStyle = c.Embeddings.Provider
	default:
		apiStyle = "openai"
	}
	prov := c.Providers[c.Embeddings.Provider]
	return apiStyle, prov.BaseURL, prov.APIKey
}

// fileConfig mirrors the mounted ConfigMap YAML file's shape. Credentials
// (PGUser/PGPassword, the provider API keys), Postgres connection fields
// (PGHost/PGPort/PGDatabase), and QdrantURL are deliberately absent here —
// they stay plain/Secret-sourced env vars, never read from this file, so
// `migrate` works before the ConfigMap exists (it's a
// pre-install/pre-upgrade hook).
type fileConfig struct {
	BindAddr string `yaml:"bindAddr"`
	Qdrant   struct {
		Collection     string  `yaml:"collection"`
		SearchMinScore float64 `yaml:"searchMinScore"`
	} `yaml:"qdrant"`
	Providers struct {
		Ollama     providerFileConfig `yaml:"ollama"`
		OpenRouter providerFileConfig `yaml:"openrouter"`
		OpenAI     providerFileConfig `yaml:"openai"`
	} `yaml:"providers"`
	Embeddings struct {
		Provider  string `yaml:"provider"`
		Model     string `yaml:"model"`
		Dimension int    `yaml:"dimension"`
	} `yaml:"embeddings"`
	Chat              purposeFileConfig `yaml:"chat"`
	GenerateTitle     purposeFileConfig `yaml:"generateTitle"`
	GenerateSummary   purposeFileConfig `yaml:"generateSummary"`
	Translate         purposeFileConfig `yaml:"translate"`
	KnowledgeLanguage string            `yaml:"knowledgeLanguage"`
}

type providerFileConfig struct {
	BaseURL                  string `yaml:"baseURL"`
	FirstTokenTimeoutSeconds int    `yaml:"firstTokenTimeoutSeconds"`
	IdleTimeoutSeconds       int    `yaml:"idleTimeoutSeconds"`
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
	f.BindAddr = "0.0.0.0:8300"
	f.Qdrant.Collection = "knowledge"
	f.Qdrant.SearchMinScore = 0.4
	f.Providers.Ollama = providerFileConfig{BaseURL: "http://localhost:11434", FirstTokenTimeoutSeconds: 300, IdleTimeoutSeconds: 60}
	f.Providers.OpenRouter = providerFileConfig{BaseURL: "https://openrouter.ai/api/v1"}
	f.Providers.OpenAI = providerFileConfig{BaseURL: "https://api.openai.com/v1"}
	f.Embeddings.Provider = "ollama"
	f.Embeddings.Model = "bge-m3"
	f.Embeddings.Dimension = 1024
	// TimeoutSeconds 1800 is the overall backstop only, as in phraseforge
	// (see specs/features/llm-streaming-progress-timeout.md).
	f.Chat = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", NumCtx: 8192, TimeoutSeconds: 1800,
		Prompt: "You answer using only the retrieved knowledge documents below.\n\nIf unsupported by the retrieved knowledge documents, say you do not know.",
	}
	f.GenerateTitle = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", TimeoutSeconds: 1800,
		Prompt: `You write a short, specific title for the given Markdown document. Respond with only the title text on a single line — no quotes, no punctuation at the end, no preamble like "Title:".`,
	}
	f.GenerateSummary = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", TimeoutSeconds: 1800,
		Prompt: `You write a one-paragraph summary of the given Markdown document, for use as a search-result preview. Respond with only the summary text — no preamble like "Summary:", no quotes.`,
	}
	f.KnowledgeLanguage = "English"
	f.Translate = purposeFileConfig{
		Provider: "ollama", Model: "gemma4:12b", TimeoutSeconds: 1800,
		Prompt: "If the following text is already in {{language}}, return it unchanged. Otherwise, translate it into {{language}}. Respond with only the resulting text — no preamble, no explanation.",
	}
	return f
}

// removedEnvVars are the per-purpose API-key variables replaced by
// per-provider ones; still setting one is an error so a key is never
// silently dropped.
var removedEnvVars = []string{"CHAT_API_KEY", "GENERATE_API_KEY", "TRANSLATE_API_KEY", "EMBEDDINGS_API_KEY"}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Load reads the mounted config file (path from CONFIG_FILE, default
// /etc/knowledge/config.yaml) over built-in defaults — a missing file is
// fine (defaults apply, so local runs without a cluster still work), but a
// malformed one, one using a removed key, or one naming an unknown
// provider is a real error, not something to silently paper over.
func Load() (Config, error) {
	fc := defaultFileConfig()
	path := env("CONFIG_FILE", "/etc/knowledge/config.yaml")
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
		if err := checkRemovedKeys(data, fc); err != nil {
			return Config{}, fmt.Errorf("config file %s: %w", path, err)
		}
	}
	if err := checkRemovedEnvVars(); err != nil {
		return Config{}, err
	}

	cfg := Config{
		BindAddr: fc.BindAddr,

		QdrantURL:        env("QDRANT_URL", "http://localhost:6333"),
		QdrantCollection: fc.Qdrant.Collection,
		SearchMinScore:   fc.Qdrant.SearchMinScore,

		PGHost:     env("PGHOST", "localhost"),
		PGPort:     env("PGPORT", "5432"),
		PGDatabase: env("PGDATABASE", "knowledge"),
		PGUser:     env("PGUSER", "postgres"),
		PGPassword: env("PGPASSWORD", ""),

		Providers: map[string]ProviderConfig{
			"ollama":     providerConfig(fc.Providers.Ollama, "OLLAMA_API_KEY"),
			"openrouter": providerConfig(fc.Providers.OpenRouter, "OPENROUTER_API_KEY"),
			"openai":     providerConfig(fc.Providers.OpenAI, "OPENAI_API_KEY"),
		},

		Embeddings: EmbeddingsConfig(fc.Embeddings),

		Chat:            PurposeConfig(fc.Chat),
		GenerateTitle:   PurposeConfig(fc.GenerateTitle),
		GenerateSummary: PurposeConfig(fc.GenerateSummary),
		Translate:       PurposeConfig(fc.Translate),

		KnowledgeLanguage: fc.KnowledgeLanguage,
	}
	if err := cfg.checkProviders(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func providerConfig(f providerFileConfig, apiKeyEnv string) ProviderConfig {
	return ProviderConfig{
		BaseURL: f.BaseURL, APIKey: env(apiKeyEnv, ""),
		FirstTokenTimeoutSeconds: f.FirstTokenTimeoutSeconds, IdleTimeoutSeconds: f.IdleTimeoutSeconds,
	}
}

// checkProviders rejects a purpose (or embeddings) naming an unregistered
// provider, reporting every such problem at once.
func (c Config) checkProviders() error {
	var errs []error
	for _, p := range []struct{ name, provider string }{
		{"chat", c.Chat.Provider}, {"generateTitle", c.GenerateTitle.Provider},
		{"generateSummary", c.GenerateSummary.Provider}, {"translate", c.Translate.Provider},
	} {
		if _, ok := c.Providers[p.provider]; !ok {
			errs = append(errs, fmt.Errorf("%s.provider %q is not in providers (ollama, openrouter, openai)", p.name, p.provider))
		}
	}
	if _, ok := c.Providers[c.Embeddings.Provider]; !ok && c.Embeddings.Provider != FakeEmbeddingsProvider {
		errs = append(errs, fmt.Errorf("embeddings.provider %q is not in providers (ollama, openrouter, openai) and is not %q", c.Embeddings.Provider, FakeEmbeddingsProvider))
	}
	return errors.Join(errs...)
}

// checkRemovedKeys rejects the pre-registry config shape, saying where each
// value moved, so an old ConfigMap with a new image fails at startup
// instead of silently using the localhost default.
func checkRemovedKeys(data []byte, fc fileConfig) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	var errs []error
	for _, sec := range []struct{ name, provider string }{
		{"embeddings", fc.Embeddings.Provider}, {"chat", fc.Chat.Provider}, {"translate", fc.Translate.Provider},
	} {
		if m, ok := raw[sec.name].(map[string]any); ok {
			if _, ok := m["baseURL"]; ok {
				errs = append(errs, fmt.Errorf("%s.baseURL is no longer supported; set providers.%s.baseURL", sec.name, sec.provider))
			}
		}
	}
	if _, ok := raw["generate"]; ok {
		errs = append(errs, errors.New("generate is no longer supported; use generateTitle and generateSummary (each with provider, model, prompt)"))
	}
	if _, ok := raw["prompts"]; ok {
		errs = append(errs, errors.New("prompts is no longer supported; move prompts.chat to chat.prompt, prompts.generateTitle to generateTitle.prompt, prompts.generateSummary to generateSummary.prompt, prompts.translate to translate.prompt"))
	}
	return errors.Join(errs...)
}

func checkRemovedEnvVars() error {
	var errs []error
	for _, name := range removedEnvVars {
		if os.Getenv(name) != "" {
			errs = append(errs, fmt.Errorf("%s is no longer supported; set the API key of the provider it was used with: OLLAMA_API_KEY, OPENROUTER_API_KEY, or OPENAI_API_KEY", name))
		}
	}
	return errors.Join(errs...)
}

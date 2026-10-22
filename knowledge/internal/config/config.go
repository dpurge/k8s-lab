package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"k8s-lab/shared/retention"
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

	Embeddings EmbeddingsConfig

	Chat            PurposeConfig
	GenerateTitle   PurposeConfig
	GenerateSummary PurposeConfig
	Translate       PurposeConfig

	KnowledgeLanguage string

	// LLMCallsRetentionDays is how long model call records are kept; 0 keeps
	// them forever.
	LLMCallsRetentionDays int
}

// PurposeConfig is one LLM use: the tags a model must have (the router picks
// the highest-priority available model with all of them; providers, models
// and their options are in models.yaml), its runtime knobs and its system
// prompt.
type PurposeConfig struct {
	ModelTags []string
	// NumCtx is the context window the purpose sizes its input to and sends
	// with each call; 0 leaves the model's own setting.
	NumCtx int
	// TimeoutSeconds caps the whole call — a backstop; the model's
	// first-token/idle limits (models.yaml) are what catch a hung call.
	TimeoutSeconds int
	Prompt         string
}

// NamedPurpose is a purpose with its key in the config file.
type NamedPurpose struct {
	Name    string
	Purpose PurposeConfig
}

// Purposes lists the four LLM purposes under their config keys.
func (c Config) Purposes() []NamedPurpose {
	return []NamedPurpose{
		{"chat", c.Chat}, {"generate_title", c.GenerateTitle},
		{"generate_summary", c.GenerateSummary}, {"translate", c.Translate},
	}
}

// DefaultLLMCallsRetentionDays is how long model call records are kept unless
// llm_calls.retention_days says otherwise.
const DefaultLLMCallsRetentionDays = 30

// DefaultNumCtx is the window assumed for a purpose that sets none: Ollama's
// own default.
const DefaultNumCtx = 4096

// EffectiveNumCtx is n, or DefaultNumCtx when n is unset (zero or negative).
func EffectiveNumCtx(n int) int {
	if n <= 0 {
		return DefaultNumCtx
	}
	return n
}

// EmbeddingsConfig names the vector space the knowledge base lives in and how
// long its vectors are. The models that produce it (providers, addresses, keys)
// are the embedding models of that space in models.yaml; the router falls back
// only between models of the same space, so the index never mixes vector spaces.
// Fake makes embeddings from a deterministic hash with no model at all, for
// tests and local runs.
type EmbeddingsConfig struct {
	Space     string
	Dimension int
	Fake      bool
}

// embeddingSpacePattern is what models.yaml accepts as an embedding_space.
var embeddingSpacePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// fileConfig mirrors the mounted ConfigMap YAML file's shape. Credentials
// (PGUser/PGPassword, the provider API keys), Postgres connection fields
// (PGHost/PGPort/PGDatabase), and QdrantURL are deliberately absent here —
// they stay plain/Secret-sourced env vars, never read from this file, so
// `migrate` works before the ConfigMap exists (it's a
// pre-install/pre-upgrade hook). Providers and models are in models.yaml.
type fileConfig struct {
	BindAddr string `yaml:"bind_addr"`
	Qdrant   struct {
		Collection     string  `yaml:"collection"`
		SearchMinScore float64 `yaml:"search_min_score"`
	} `yaml:"qdrant"`
	Embeddings struct {
		Space     string `yaml:"embedding_space"`
		Dimension int    `yaml:"dimension"`
		Fake      bool   `yaml:"fake"`
	} `yaml:"embeddings"`
	Chat              purposeFileConfig `yaml:"chat"`
	GenerateTitle     purposeFileConfig `yaml:"generate_title"`
	GenerateSummary   purposeFileConfig `yaml:"generate_summary"`
	Translate         purposeFileConfig `yaml:"translate"`
	KnowledgeLanguage string            `yaml:"knowledge_language"`
	LLMCalls          struct {
		RetentionDays int `yaml:"retention_days"`
	} `yaml:"llm_calls"`
}

type purposeFileConfig struct {
	ModelTags      []string `yaml:"model_tags"`
	NumCtx         int      `yaml:"num_ctx"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
	Prompt         string   `yaml:"prompt"`
}

func defaultFileConfig() fileConfig {
	var f fileConfig
	f.BindAddr = "0.0.0.0:8300"
	f.Qdrant.Collection = "knowledge"
	f.Qdrant.SearchMinScore = 0.4
	f.Embeddings.Space = "bge-m3"
	f.Embeddings.Dimension = 1024
	// TimeoutSeconds 1800 is the overall backstop only, as in phraseforge
	// (see specs/features/llm-streaming-progress-timeout.md).
	f.Chat = purposeFileConfig{
		ModelTags: []string{"chat"}, NumCtx: 8192, TimeoutSeconds: 1800,
		Prompt: "You answer using only the retrieved knowledge documents below.\n\nIf unsupported by the retrieved knowledge documents, say you do not know.",
	}
	f.GenerateTitle = purposeFileConfig{
		ModelTags: []string{"default"}, TimeoutSeconds: 1800,
		Prompt: `You write a short, specific title for the given Markdown document. Respond with only the title text on a single line — no quotes, no punctuation at the end, no preamble like "Title:".`,
	}
	f.GenerateSummary = purposeFileConfig{
		ModelTags: []string{"default"}, TimeoutSeconds: 1800,
		Prompt: `You write a one-paragraph summary of the given Markdown document, for use as a search-result preview. Respond with only the summary text — no preamble like "Summary:", no quotes.`,
	}
	f.KnowledgeLanguage = "English"
	f.LLMCalls.RetentionDays = DefaultLLMCallsRetentionDays
	f.Translate = purposeFileConfig{
		ModelTags: []string{"default"}, TimeoutSeconds: 1800,
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
// malformed one, one using a removed or unknown key, or invalid values is a
// real error, not something to silently paper over. The models the purposes
// run on are in models.yaml (see LoadModels), which only the server needs.
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
		if err := checkRemovedKeys(data); err != nil {
			return Config{}, fmt.Errorf("config file %s: %w", path, err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
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

		Embeddings: EmbeddingsConfig(fc.Embeddings),

		Chat:            PurposeConfig(fc.Chat),
		GenerateTitle:   PurposeConfig(fc.GenerateTitle),
		GenerateSummary: PurposeConfig(fc.GenerateSummary),
		Translate:       PurposeConfig(fc.Translate),

		KnowledgeLanguage: fc.KnowledgeLanguage,

		LLMCallsRetentionDays: fc.LLMCalls.RetentionDays,
	}
	if err := cfg.check(); err != nil {
		return Config{}, fmt.Errorf("config file %s: %w", path, err)
	}
	return cfg, nil
}

// check reports every invalid value at once.
func (c Config) check() error {
	var errs []error
	for _, np := range c.Purposes() {
		if len(np.Purpose.ModelTags) == 0 {
			errs = append(errs, fmt.Errorf("%s.model_tags must list at least one tag", np.Name))
		}
		seen := map[string]bool{}
		for _, tag := range np.Purpose.ModelTags {
			switch {
			case strings.TrimSpace(tag) == "":
				errs = append(errs, fmt.Errorf("%s.model_tags has a blank tag", np.Name))
			case seen[tag]:
				errs = append(errs, fmt.Errorf("%s.model_tags lists %q twice", np.Name, tag))
			}
			seen[tag] = true
		}
		if np.Purpose.NumCtx < 0 {
			errs = append(errs, fmt.Errorf("%s.num_ctx must not be negative", np.Name))
		}
		if np.Purpose.TimeoutSeconds < 0 {
			errs = append(errs, fmt.Errorf("%s.timeout_seconds must not be negative", np.Name))
		}
	}
	if !embeddingSpacePattern.MatchString(c.Embeddings.Space) {
		errs = append(errs, fmt.Errorf("embeddings.embedding_space %q must start with a lowercase letter and use only lowercase letters, digits and hyphens", c.Embeddings.Space))
	}
	if c.LLMCallsRetentionDays < 0 || c.LLMCallsRetentionDays > retention.MaxDays {
		errs = append(errs, fmt.Errorf("llm_calls.retention_days must be a whole number of days from 0 (keep forever) to %d, got %d", retention.MaxDays, c.LLMCallsRetentionDays))
	}
	if c.Embeddings.Dimension < 1 {
		errs = append(errs, fmt.Errorf("embeddings.dimension must be at least 1, got %d", c.Embeddings.Dimension))
	}
	return errors.Join(errs...)
}

// renamedKeys maps an old camelCase key (a dotted path) to what replaced it.
var renamedKeys = map[string]string{
	"bindAddr":              "bind_addr",
	"knowledgeLanguage":     "knowledge_language",
	"generateTitle":         "generate_title",
	"generateSummary":       "generate_summary",
	"qdrant.searchMinScore": "qdrant.search_min_score",
}

// embeddingsRemovedKeys are the keys of the embeddings block that models.yaml
// replaced, by old name.
var embeddingsRemovedKeys = map[string]string{
	"provider": "embeddings.embedding_space (the providers of an embedding space are in models.yaml)",
	"model":    "embeddings.embedding_space and an embedding model in models.yaml",
	"baseURL":  "the base_url of a provider in models.yaml",
	"base_url": "the base_url of a provider in models.yaml",
}

// purposeRenamedKeys are the renamed or moved keys of each purpose section
// (chat, generate_title, generate_summary, translate), by old name.
var purposeRenamedKeys = map[string]string{
	"provider":       "model_tags (the router picks the provider; providers and models are in models.yaml)",
	"model":          "model_tags (the router picks the model; providers and models are in models.yaml)",
	"numCtx":         "num_ctx",
	"timeoutSeconds": "timeout_seconds",
	"think":          "the think option of a model in models.yaml",
	"baseURL":        "the base_url of a provider in models.yaml",
}

// checkRemovedKeys rejects the earlier config shapes, saying where each value
// moved, so an old ConfigMap with a new image fails at startup instead of
// silently using a default. It reports every such key at once.
func checkRemovedKeys(data []byte) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	var errs []error
	moved := func(old, now string) {
		errs = append(errs, fmt.Errorf("%s is no longer supported; use %s", old, now))
	}
	for old, now := range renamedKeys {
		path := strings.Split(old, ".")
		if len(path) == 1 {
			if _, ok := raw[old]; ok {
				moved(old, now)
			}
			continue
		}
		if m, ok := raw[path[0]].(map[string]any); ok {
			if _, ok := m[path[1]]; ok {
				moved(old, now)
			}
		}
	}
	if _, ok := raw["providers"]; ok {
		errs = append(errs, errors.New("providers is no longer supported; providers, their base_url and the streaming limits (first_token_timeout_seconds, idle_timeout_seconds) are in models.yaml"))
	}
	for _, section := range []string{"chat", "generateTitle", "generateSummary", "generate_title", "generate_summary", "translate"} {
		m, ok := raw[section].(map[string]any)
		if !ok {
			continue
		}
		shown := section
		if now, renamed := renamedKeys[section]; renamed {
			shown = now // the section itself is reported above; name its new key here
		}
		for old, now := range purposeRenamedKeys {
			if _, ok := m[old]; ok {
				moved(shown+"."+old, now)
			}
		}
	}
	if m, ok := raw["embeddings"].(map[string]any); ok {
		for old, now := range embeddingsRemovedKeys {
			if _, ok := m[old]; ok {
				moved("embeddings."+old, now)
			}
		}
	}
	if _, ok := raw["generate"]; ok {
		errs = append(errs, errors.New("generate is no longer supported; use generate_title and generate_summary (each with model_tags, prompt)"))
	}
	if _, ok := raw["prompts"]; ok {
		errs = append(errs, errors.New("prompts is no longer supported; move prompts.chat to chat.prompt, prompts.generateTitle to generate_title.prompt, prompts.generateSummary to generate_summary.prompt, prompts.translate to translate.prompt"))
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errors.Join(errs...)
}

func checkRemovedEnvVars() error {
	var errs []error
	for _, name := range removedEnvVars {
		if os.Getenv(name) != "" {
			errs = append(errs, fmt.Errorf("%s is no longer supported; set the API key of the provider it was used with: OLLAMA_API_KEY, OPENROUTER_API_KEY, or OPENAI_API_KEY (for models.yaml: api_key_from: env:NAME)", name))
		}
	}
	return errors.Join(errs...)
}

package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
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

	EmbeddingsProvider  string
	EmbeddingsBaseURL   string
	EmbeddingsAPIKey    string
	EmbeddingsModel     string
	EmbeddingsDimension int

	ChatProvider string
	ChatBaseURL  string
	ChatAPIKey   string
	ChatModel    string
	ChatNumCtx   int

	GenerateProvider string
	GenerateBaseURL  string
	GenerateAPIKey   string
	GenerateModel    string
	GenerateNumCtx   int

	KnowledgeLanguage string

	TranslateProvider string
	TranslateBaseURL  string
	TranslateAPIKey   string
	TranslateModel    string
	TranslateNumCtx   int

	ChatPrompt            string
	GenerateTitlePrompt   string
	GenerateSummaryPrompt string
	TranslatePrompt       string
}

// fileConfig mirrors the mounted ConfigMap YAML file's shape. Credentials
// (PGUser/PGPassword, the *APIKey fields) are deliberately absent here —
// they stay plain/Secret-sourced env vars, never read from this file.
type fileConfig struct {
	BindAddr string `yaml:"bindAddr"`
	Qdrant   struct {
		URL            string  `yaml:"url"`
		Collection     string  `yaml:"collection"`
		SearchMinScore float64 `yaml:"searchMinScore"`
	} `yaml:"qdrant"`
	Postgres struct {
		Host     string `yaml:"host"`
		Port     string `yaml:"port"`
		Database string `yaml:"database"`
	} `yaml:"postgres"`
	Embeddings struct {
		Provider  string `yaml:"provider"`
		BaseURL   string `yaml:"baseURL"`
		Model     string `yaml:"model"`
		Dimension int    `yaml:"dimension"`
	} `yaml:"embeddings"`
	Chat struct {
		Provider string `yaml:"provider"`
		BaseURL  string `yaml:"baseURL"`
		Model    string `yaml:"model"`
		NumCtx   int    `yaml:"numCtx"`
	} `yaml:"chat"`
	Generate struct {
		Provider string `yaml:"provider"`
		BaseURL  string `yaml:"baseURL"`
		Model    string `yaml:"model"`
		NumCtx   int    `yaml:"numCtx"`
	} `yaml:"generate"`
	KnowledgeLanguage string `yaml:"knowledgeLanguage"`
	Translate         struct {
		Provider string `yaml:"provider"`
		BaseURL  string `yaml:"baseURL"`
		Model    string `yaml:"model"`
		NumCtx   int    `yaml:"numCtx"`
	} `yaml:"translate"`
	Prompts struct {
		Chat            string `yaml:"chat"`
		GenerateTitle   string `yaml:"generateTitle"`
		GenerateSummary string `yaml:"generateSummary"`
		Translate       string `yaml:"translate"`
	} `yaml:"prompts"`
}

func defaultFileConfig() fileConfig {
	var f fileConfig
	f.BindAddr = "0.0.0.0:8300"
	f.Qdrant.URL = "http://localhost:6333"
	f.Qdrant.Collection = "knowledge"
	f.Qdrant.SearchMinScore = 0.4
	f.Postgres.Host = "localhost"
	f.Postgres.Port = "5432"
	f.Postgres.Database = "knowledge"
	f.Embeddings.Provider = "ollama"
	f.Embeddings.BaseURL = "http://localhost:11434"
	f.Embeddings.Model = "bge-m3"
	f.Embeddings.Dimension = 1024
	f.Chat.Provider = "ollama"
	f.Chat.BaseURL = "http://localhost:11434"
	f.Chat.Model = "gemma4:12b"
	f.Chat.NumCtx = 8192
	f.Generate.Provider = "ollama"
	f.Generate.BaseURL = "http://localhost:11434"
	f.Generate.Model = "gemma4:12b"
	f.KnowledgeLanguage = "English"
	f.Translate.Provider = "ollama"
	f.Translate.BaseURL = "http://localhost:11434"
	f.Translate.Model = "gemma4:12b"
	f.Prompts.Chat = "You answer using only the retrieved knowledge documents below.\n\nIf unsupported by the retrieved knowledge documents, say you do not know."
	f.Prompts.GenerateTitle = `You write a short, specific title for the given Markdown document. Respond with only the title text on a single line — no quotes, no punctuation at the end, no preamble like "Title:".`
	f.Prompts.GenerateSummary = `You write a one-paragraph summary of the given Markdown document, for use as a search-result preview. Respond with only the summary text — no preamble like "Summary:", no quotes.`
	f.Prompts.Translate = "If the following text is already in {{language}}, return it unchanged. Otherwise, translate it into {{language}}. Respond with only the resulting text — no preamble, no explanation."
	return f
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Load reads the mounted config file (path from CONFIG_FILE, default
// /etc/knowledge/config.yaml) over built-in defaults — a missing file is
// fine (defaults apply, so local runs without a cluster still work), but a
// malformed one is a real error, not something to silently paper over.
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
	}

	return Config{
		BindAddr: fc.BindAddr,

		QdrantURL:        fc.Qdrant.URL,
		QdrantCollection: fc.Qdrant.Collection,
		SearchMinScore:   fc.Qdrant.SearchMinScore,

		PGHost:     fc.Postgres.Host,
		PGPort:     fc.Postgres.Port,
		PGDatabase: fc.Postgres.Database,
		PGUser:     env("PGUSER", "postgres"),
		PGPassword: env("PGPASSWORD", ""),

		EmbeddingsProvider:  fc.Embeddings.Provider,
		EmbeddingsBaseURL:   fc.Embeddings.BaseURL,
		EmbeddingsAPIKey:    env("EMBEDDINGS_API_KEY", ""),
		EmbeddingsModel:     fc.Embeddings.Model,
		EmbeddingsDimension: fc.Embeddings.Dimension,

		ChatProvider: fc.Chat.Provider,
		ChatBaseURL:  fc.Chat.BaseURL,
		ChatAPIKey:   env("CHAT_API_KEY", ""),
		ChatModel:    fc.Chat.Model,
		ChatNumCtx:   fc.Chat.NumCtx,

		GenerateProvider: fc.Generate.Provider,
		GenerateBaseURL:  fc.Generate.BaseURL,
		GenerateAPIKey:   env("GENERATE_API_KEY", ""),
		GenerateModel:    fc.Generate.Model,
		GenerateNumCtx:   fc.Generate.NumCtx,

		KnowledgeLanguage: fc.KnowledgeLanguage,

		TranslateProvider: fc.Translate.Provider,
		TranslateBaseURL:  fc.Translate.BaseURL,
		TranslateAPIKey:   env("TRANSLATE_API_KEY", ""),
		TranslateModel:    fc.Translate.Model,
		TranslateNumCtx:   fc.Translate.NumCtx,

		ChatPrompt:            fc.Prompts.Chat,
		GenerateTitlePrompt:   fc.Prompts.GenerateTitle,
		GenerateSummaryPrompt: fc.Prompts.GenerateSummary,
		TranslatePrompt:       fc.Prompts.Translate,
	}, nil
}

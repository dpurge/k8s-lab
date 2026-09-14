package config

import (
	"os"
	"strconv"
)

type Config struct {
	BindAddr string

	QdrantURL        string
	QdrantCollection string

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
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func Load() Config {
	return Config{
		BindAddr: env("BIND_ADDR", "0.0.0.0:8300"),

		QdrantURL:        env("QDRANT_URL", "http://localhost:6333"),
		QdrantCollection: env("QDRANT_COLLECTION", "knowledge"),

		PGHost:     env("PGHOST", "localhost"),
		PGPort:     env("PGPORT", "5432"),
		PGDatabase: env("PGDATABASE", "knowledge"),
		PGUser:     env("PGUSER", "postgres"),
		PGPassword: env("PGPASSWORD", ""),

		EmbeddingsProvider:  env("EMBEDDINGS_PROVIDER", "ollama"),
		EmbeddingsBaseURL:   env("EMBEDDINGS_BASE_URL", "http://localhost:11434"),
		EmbeddingsAPIKey:    env("EMBEDDINGS_API_KEY", ""),
		EmbeddingsModel:     env("EMBEDDINGS_MODEL", "nomic-embed-text"),
		EmbeddingsDimension: envInt("EMBEDDINGS_DIMENSION", 768),

		ChatProvider: env("CHAT_PROVIDER", "ollama"),
		ChatBaseURL:  env("CHAT_BASE_URL", "http://localhost:11434"),
		ChatAPIKey:   env("CHAT_API_KEY", ""),
		ChatModel:    env("CHAT_MODEL", "gemma4:e4b"),
	}
}

// Package config loads dictionary's runtime configuration from environment
// variables, matching phraseforge's PG* names so both plug into the same
// postgres-credentials Secret.
package config

import "os"

type Config struct {
	BindAddr   string
	PGHost     string
	PGPort     string
	PGDatabase string
	PGUser     string
	PGPassword string
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func Load() Config {
	return Config{
		BindAddr:   env("BIND_ADDR", "0.0.0.0:8100"),
		PGHost:     env("PGHOST", "localhost"),
		PGPort:     env("PGPORT", "5432"),
		PGDatabase: env("PGDATABASE", "dictionary"),
		PGUser:     env("PGUSER", "phraseforge"),
		PGPassword: env("PGPASSWORD", ""),
	}
}

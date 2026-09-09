package config

import "os"

// Config holds everything the server needs to start.
type Config struct {
	BindAddr   string
	PGHost     string
	PGPort     string
	PGDatabase string
	PGUser     string
	PGPassword string
	// SessionKey signs the login session cookie. Must be set in production;
	// a fixed dev default is used only so `go run` works with zero setup.
	SessionKey string
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Load reads Config from environment variables, matching phraseforge-api's names.
func Load() Config {
	return Config{
		BindAddr:   env("BIND_ADDR", "0.0.0.0:8090"),
		PGHost:     env("PGHOST", "localhost"),
		PGPort:     env("PGPORT", "5432"),
		PGDatabase: env("PGDATABASE", "phraseforge_app"),
		PGUser:     env("PGUSER", "phraseforge"),
		PGPassword: env("PGPASSWORD", ""),
		SessionKey: env("SESSION_KEY", "dev-only-insecure-key-change-me"),
	}
}

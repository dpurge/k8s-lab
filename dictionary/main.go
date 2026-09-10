// Command dictionary runs the dictionary API server, or applies its database
// schema, matching phraseforge's binary/migrate-job split.
//
// Usage:
//
//	dictionary serve    # start the HTTP server (default if no subcommand given)
//	dictionary migrate  # create the database (if missing) and apply schema.sql
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"dictionary/internal/auth"
	"dictionary/internal/config"
	"dictionary/internal/db"
	"dictionary/internal/entries"
	"dictionary/internal/inflection"
	"dictionary/internal/languages"
	"dictionary/internal/roles"
	"dictionary/internal/server"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "migrate":
		if err := db.Migrate(ctx, cfg); err != nil {
			log.Fatalf("migrate: %v", err)
		}
	case "serve":
		runServe(ctx, cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (expected serve|migrate)\n", cmd)
		os.Exit(1)
	}
}

func runServe(ctx context.Context, cfg config.Config) {
	pool, err := db.Connect(ctx, cfg)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	rolesSvc := roles.New(pool)
	authSvc := auth.New(pool, rolesSvc)
	if err := authSvc.EnsureBootstrapAdmin(ctx, "admin", "dictionary-admin"); err != nil {
		log.Fatalf("bootstrap admin user: %v", err)
	}

	languagesSvc := languages.New(pool)
	entriesSvc := entries.New(pool, languagesSvc)
	inflectionSvc := inflection.New(pool, languagesSvc)
	srv := server.New(pool, authSvc, rolesSvc, languagesSvc, entriesSvc, inflectionSvc)

	log.Printf("dictionary listening on %s", cfg.BindAddr)
	log.Fatal(http.ListenAndServe(cfg.BindAddr, srv.Router()))
}

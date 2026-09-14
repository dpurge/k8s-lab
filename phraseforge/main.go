// Command phraseforge runs the PhraseForge server, or applies its database
// schema, matching phraseforge-api's binary/migrate-job split.
//
// Usage:
//
//	phraseforge serve    # start the HTTP server (default if no subcommand given)
//	phraseforge migrate  # create the database (if missing) and apply schema.sql
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"k8s-lab/shared/llm"

	"phraseforge/internal/ai"
	"phraseforge/internal/auth"
	"phraseforge/internal/config"
	"phraseforge/internal/db"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/models"
	"phraseforge/internal/roles"
	"phraseforge/internal/server"
	"phraseforge/internal/tags"
	"phraseforge/internal/texts"
	"phraseforge/internal/translations"
	"phraseforge/internal/vocabulary"
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

	authSvc := auth.New(pool, cfg.SessionKey)
	// Dev/first-run convenience: bootstrap an admin user if the table is empty,
	// same idea as Readeck's first-run admin creation. Credentials are logged
	// once so they're never silently unknown; change them after first login.
	if err := authSvc.EnsureUser(ctx, "admin", "phraseforge"); err != nil {
		log.Fatalf("bootstrap admin user: %v", err)
	}

	textStore := texts.New(pool)
	dialogStore := dialogs.New(pool)
	vocabStore := vocabulary.New(pool)
	modelsStore := models.New(pool)
	rolesSvc := roles.New(pool)
	tagsSvc := tags.New(pool)
	translationsSvc := translations.New(pool)
	aiSvc := ai.New(pool, llm.Config{Provider: cfg.LLMProvider, BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey, Model: cfg.LLMModel})
	srv := server.New(pool, authSvc, textStore, dialogStore, vocabStore, modelsStore, rolesSvc, tagsSvc, translationsSvc, aiSvc)

	log.Printf("phraseforge listening on %s, llm=%s/%s", cfg.BindAddr, cfg.LLMProvider, cfg.LLMModel)
	log.Fatal(http.ListenAndServe(cfg.BindAddr, srv.Router()))
}

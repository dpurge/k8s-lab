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

	"phraseforge/internal/ai"
	"phraseforge/internal/auth"
	"phraseforge/internal/config"
	"phraseforge/internal/db"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/generate"
	"phraseforge/internal/ingest"
	"phraseforge/internal/jobs"
	"phraseforge/internal/models"
	"phraseforge/internal/roles"
	"phraseforge/internal/server"
	"phraseforge/internal/tags"
	"phraseforge/internal/texts"
	"phraseforge/internal/translations"
	"phraseforge/internal/vocabulary"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
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

// itemTranslationWriteback adapts *vocabulary.Store and *models.Store to
// ai.ItemTranslationWriteback: HandleGenerate's item-level translation
// writeback needs one interface that dispatches to whichever store owns
// resourceType's rows, since vocabulary and models expose differently-shaped
// guarded translation setters — vocabulary.Store.SetItemTranslationGuarded
// also carries a notes field (fetched and preserved here, see below),
// models.Store.SetItemTranslationGuarded has none. This is the one place
// that imports both stores just to switch between them, mirroring this
// codebase's "define a small interface, satisfy it with an adapter built
// where both concrete types are already imported" convention.
type itemTranslationWriteback struct {
	vocab  *vocabulary.Store
	models *models.Store
}

func (w *itemTranslationWriteback) SetItemTranslation(ctx context.Context, resourceType string, listID int64, position int, phrase, locale, translation string) error {
	switch resourceType {
	case "vocabulary_item":
		// Vocabulary's guarded setter upserts translation AND notes together
		// (its normal caller is a full edit-form submission that always has
		// both) — a translation-only writeback must preserve whatever notes
		// are already stored at this position/locale rather than blanking
		// them, so fetch them first. SetItemTranslationGuarded (not the
		// plain SetTranslations) applies phrase as a stale-target guard: a
		// job generated for this (listID, position, phrase) triple must not
		// silently overwrite a different item that has since taken that
		// position (see ai.ItemTranslationWriteback's doc comment, B3 fix).
		existing, err := w.vocab.Translations(ctx, listID, locale)
		if err != nil {
			return err
		}
		return w.vocab.SetItemTranslationGuarded(ctx, listID, position, phrase, locale, translation, existing[position].Notes)
	case "models_item":
		return w.models.SetItemTranslationGuarded(ctx, listID, position, phrase, locale, translation)
	default:
		return fmt.Errorf("phraseforge: unknown resource_type %q for item translation writeback", resourceType)
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
	itemTranslationsSvc := &itemTranslationWriteback{vocab: vocabStore, models: modelsStore}
	aiSvc := ai.New(pool, cfg, textStore, dialogStore, translationsSvc, vocabStore, modelsStore, itemTranslationsSvc)

	// jobsSvc is phraseforge's single app-wide job queue (see
	// specs/features/phraseforge-job-queue.md): /llm/generate enqueues its
	// LLM-calling work onto it rather than calling ai.Service.Generate
	// directly, and its one worker goroutine (started below) is the only
	// thing in this process that ever makes such a call at a time —
	// mirrors knowledge/main.go's exact queue wiring order.
	jobsSvc := jobs.New(pool)
	if err := jobsSvc.FailStale(ctx); err != nil {
		log.Fatalf("fail stale jobs: %v", err)
	}
	jobsSvc.Register(ai.KindLLMGenerate, aiSvc.HandleGenerate)

	// ingestSvc's two job kinds turn an ingest HTTP endpoint's staged raw
	// content (a later pass — see specs/features/phraseforge-ingest-texts-dialogs.md)
	// into a real Text/Dialog row; registered here alongside KindLLMGenerate,
	// before the worker goroutine starts.
	ingestSvc := ingest.New(textStore, dialogStore, jobsSvc, aiSvc, pool)
	jobsSvc.Register(ingest.KindProcessText, ingestSvc.HandleProcessText)
	jobsSvc.Register(ingest.KindProcessDialog, ingestSvc.HandleProcessDialog)

	// generateSvc's two job kinds back the Texts view page's "Generate
	// Vocabulary"/"Generate Models" buttons (see specs/features/
	// phraseforge-generate-vocab-models-from-text.md); registered here
	// alongside the other job kinds, before the worker goroutine starts.
	generateSvc := generate.New(textStore, vocabStore, modelsStore, aiSvc)
	jobsSvc.Register(generate.KindGenerateVocabFromText, generateSvc.HandleGenerateVocabFromText)
	jobsSvc.Register(generate.KindGenerateModelsFromText, generateSvc.HandleGenerateModelsFromText)

	go jobsSvc.Run(ctx)

	srv := server.New(pool, authSvc, textStore, dialogStore, vocabStore, modelsStore, rolesSvc, tagsSvc, translationsSvc, aiSvc, jobsSvc)

	log.Printf("phraseforge listening on %s, transcription=%s/%s, translation=%s/%s", cfg.BindAddr, cfg.Transcription.Provider, cfg.Transcription.Model, cfg.Translation.Provider, cfg.Translation.Model)
	log.Fatal(http.ListenAndServe(cfg.BindAddr, srv.Router()))
}

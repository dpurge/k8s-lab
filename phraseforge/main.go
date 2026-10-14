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
	"encoding/json"
	"errors"
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
	"phraseforge/internal/resreview"
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

// itemStore adapts *vocabulary.Store and *models.Store to ai.ItemStore:
// HandleItemTranslation reads and writes an item through one interface that
// dispatches to whichever store owns resourceType's rows, since the two expose
// differently-shaped setters (vocabulary has notes and grammar, models has
// neither). This is the one place that imports both stores just to switch
// between them, mirroring this codebase's "define a small interface, satisfy
// it with an adapter built where both concrete types are already imported"
// convention. It also maps the stores' item-gone and item-changed errors to
// ai.ErrItemGone and ai.ErrItemChanged, which end the job as skipped.
type itemStore struct {
	vocab  *vocabulary.Store
	models *models.Store
}

func (w *itemStore) ItemSource(ctx context.Context, resourceType string, itemID int64) (ai.ItemSource, error) {
	switch resourceType {
	case "vocabulary_item":
		src, err := w.vocab.ItemSource(ctx, itemID)
		return ai.ItemSource{Phrase: src.Phrase, Language: src.Language, Examples: src.Examples, Translations: src.Translations}, skipOnStaleItem(err)
	case "models_item":
		src, err := w.models.ItemSource(ctx, itemID)
		return ai.ItemSource{Phrase: src.Phrase, Language: src.Language}, skipOnStaleItem(err)
	default:
		return ai.ItemSource{}, fmt.Errorf("phraseforge: unknown resource_type %q for item source", resourceType)
	}
}

func (w *itemStore) FillTranslation(ctx context.Context, resourceType string, itemID int64, phrase, locale, translation, notes string) (bool, error) {
	switch resourceType {
	case "vocabulary_item":
		applied, err := w.vocab.FillTranslation(ctx, itemID, phrase, locale, translation, notes)
		return applied, skipOnStaleItem(err)
	case "models_item":
		applied, err := w.models.FillTranslation(ctx, itemID, phrase, locale, translation)
		return applied, skipOnStaleItem(err)
	default:
		return false, fmt.Errorf("phraseforge: unknown resource_type %q for item translation", resourceType)
	}
}

func (w *itemStore) FillTranscription(ctx context.Context, resourceType string, itemID int64, phrase, transcription string) (bool, error) {
	switch resourceType {
	case "vocabulary_item":
		applied, err := w.vocab.FillTranscription(ctx, itemID, phrase, transcription)
		return applied, skipOnStaleItem(err)
	case "models_item":
		applied, err := w.models.FillTranscription(ctx, itemID, phrase, transcription)
		return applied, skipOnStaleItem(err)
	default:
		return false, fmt.Errorf("phraseforge: unknown resource_type %q for item transcription", resourceType)
	}
}

// FillGrammar: only vocabulary items have grammar.
func (w *itemStore) FillGrammar(ctx context.Context, itemID int64, phrase, grammar string) (bool, error) {
	applied, err := w.vocab.FillGrammar(ctx, itemID, phrase, grammar)
	return applied, skipOnStaleItem(err)
}

// FillExample: only vocabulary items have examples.
func (w *itemStore) FillExample(ctx context.Context, itemID int64, phrase, example string) (bool, error) {
	applied, err := w.vocab.FillExample(ctx, itemID, phrase, example)
	return applied, skipOnStaleItem(err)
}

// AppendSense: only vocabulary items have senses to append to.
func (w *itemStore) AppendSense(ctx context.Context, itemID int64, phrase, locale, sense string) (bool, error) {
	applied, err := w.vocab.AppendSense(ctx, itemID, phrase, locale, sense)
	return applied, skipOnStaleItem(err)
}

// RewritePhrase: only vocabulary items have a rewritable phrase.
func (w *itemStore) RewritePhrase(ctx context.Context, itemID int64, phrase, newPhrase, grammar, transcription string) (bool, error) {
	applied, err := w.vocab.RewritePhrase(ctx, itemID, phrase, newPhrase, grammar, transcription)
	return applied, skipOnStaleItem(err)
}

// reviewStore adapts *vocabulary.Store to ai.ReviewStore, mapping the store's
// item-gone and item-changed errors to the ai errors that skip the job.
type reviewStore struct {
	vocab *vocabulary.Store
}

func (w *reviewStore) ReviewSource(ctx context.Context, itemID int64) (ai.ReviewInput, error) {
	src, err := w.vocab.ReviewSource(ctx, itemID)
	if err != nil {
		return ai.ReviewInput{}, skipOnStaleItem(err)
	}
	in := ai.ReviewInput{Language: src.Language, Phrase: src.Phrase, Grammar: src.Grammar, Transcription: src.Transcription, Examples: src.Examples}
	for _, t := range src.Translations {
		in.Translations = append(in.Translations, ai.ReviewTranslation{Locale: t.Locale, Translation: t.Translation, Notes: t.Notes})
	}
	return in, nil
}

func (w *reviewStore) SaveReview(ctx context.Context, itemID int64, phrase string, findings, response json.RawMessage) error {
	return skipOnStaleItem(w.vocab.SaveReview(ctx, itemID, phrase, findings, response))
}

// modelsReviewStore adapts *models.Store to ai.ReviewStore, like reviewStore.
type modelsReviewStore struct {
	models *models.Store
}

func (w *modelsReviewStore) ReviewSource(ctx context.Context, itemID int64) (ai.ReviewInput, error) {
	src, err := w.models.ReviewSource(ctx, itemID)
	if err != nil {
		return ai.ReviewInput{}, skipOnStaleItem(err)
	}
	in := ai.ReviewInput{Language: src.Language, Phrase: src.Phrase, Transcription: src.Transcription}
	for _, t := range src.Translations {
		in.Translations = append(in.Translations, ai.ReviewTranslation{Locale: t.Locale, Translation: t.Translation})
	}
	return in, nil
}

func (w *modelsReviewStore) SaveReview(ctx context.Context, itemID int64, phrase string, findings, response json.RawMessage) error {
	return skipOnStaleItem(w.models.SaveReview(ctx, itemID, phrase, findings, response))
}

// skipOnStaleItem turns either store's item-gone or item-changed error into
// the ai error that skips the job; any other error is returned unchanged.
func skipOnStaleItem(err error) error {
	switch {
	case errors.Is(err, vocabulary.ErrItemNotFound), errors.Is(err, models.ErrItemNotFound):
		return ai.ErrItemGone
	case errors.Is(err, vocabulary.ErrItemChanged), errors.Is(err, models.ErrItemChanged):
		return ai.ErrItemChanged
	default:
		return err
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
	itemsSvc := &itemStore{vocab: vocabStore, models: modelsStore}
	aiSvc := ai.New(pool, cfg, textStore, dialogStore, translationsSvc, itemsSvc)
	aiSvc.SetReviewStore(&reviewStore{vocab: vocabStore})
	aiSvc.SetModelsReviewStore(&modelsReviewStore{models: modelsStore})
	aiSvc.SetTextReviewStore(resreview.New(pool))

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
	// KindLLMGenerate is kept registered for historical rows; new callers
	// enqueue under ai.JobKind's field-specific kinds instead (see that
	// function's doc comment) — all four run the exact same handler.
	jobsSvc.Register(ai.KindLLMGenerate, aiSvc.HandleGenerate)
	jobsSvc.Register(ai.KindGenerateTitle, aiSvc.HandleGenerate)
	jobsSvc.Register(ai.KindGenerateTranscription, aiSvc.HandleGenerate)
	jobsSvc.Register(ai.KindGenerateTranslation, aiSvc.HandleGenerate)
	jobsSvc.Register(ai.KindGenerateVocabularyItem, aiSvc.HandleItemTranslation)
	jobsSvc.Register(ai.KindGenerateModelsItem, aiSvc.HandleItemTranslation)
	jobsSvc.Register(ai.KindReviewVocabularyItem, aiSvc.HandleReviewItem)
	jobsSvc.Register(ai.KindReviewModelsItem, aiSvc.HandleReviewModelsItem)
	jobsSvc.Register(ai.KindReviewTextJob, aiSvc.HandleReviewResource)

	// ingestSvc's two job kinds turn an ingest HTTP endpoint's staged raw
	// content (a later pass — see specs/features/phraseforge-ingest-texts-dialogs.md)
	// into a real Text/Dialog row; registered here alongside KindLLMGenerate,
	// before the worker goroutine starts.
	ingestSvc := ingest.New(textStore, dialogStore, jobsSvc, aiSvc, pool)
	jobsSvc.Register(ingest.KindProcessText, ingestSvc.HandleProcessText)
	jobsSvc.Register(ingest.KindProcessDialog, ingestSvc.HandleProcessDialog)

	// generateSvc's job kinds back the Texts/Dialogs view pages' "Generate
	// Vocabulary"/"Generate Models" buttons (see specs/features/
	// phraseforge-generate-vocab-models-from-text.md and
	// dialog-vocabulary-models-generation.md); registered here alongside the
	// other job kinds, before the worker goroutine starts. The Text/Dialog
	// kind pair for each of Vocab/Models shares one handler (dispatched on
	// the payload's resource_type) but keeps four distinct kind values so
	// the Jobs page and Retry still show/route on what actually ran.
	generateSvc := generate.New(textStore, dialogStore, vocabStore, modelsStore, aiSvc, jobsSvc, pool)
	generateSvc.SetChecker(generate.AICheck{AI: aiSvc})
	jobsSvc.Register(generate.KindGenerateVocabFromText, generateSvc.HandleGenerateVocab)
	jobsSvc.Register(generate.KindGenerateVocabFromDialog, generateSvc.HandleGenerateVocab)
	jobsSvc.Register(generate.KindGenerateModelsFromText, generateSvc.HandleGenerateModels)
	jobsSvc.Register(generate.KindGenerateModelsFromDialog, generateSvc.HandleGenerateModels)

	go jobsSvc.Run(ctx)

	srv := server.New(pool, authSvc, textStore, dialogStore, vocabStore, modelsStore, rolesSvc, tagsSvc, translationsSvc, aiSvc, jobsSvc)
	srv.SetIngestMaxContentBytes(cfg.IngestMaxContentBytes)

	log.Printf("phraseforge listening on %s, transcription=%s/%s, translation=%s/%s", cfg.BindAddr, cfg.Transcription.Provider, cfg.Transcription.Model, cfg.Translation.Provider, cfg.Translation.Model)
	log.Fatal(http.ListenAndServe(cfg.BindAddr, srv.Router()))
}

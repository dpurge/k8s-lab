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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/retention"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/anthropic"
	"k8s-lab/shared/router/ollama"
	"k8s-lab/shared/router/openai"
	"k8s-lab/shared/router/vertex"

	"phraseforge/internal/ai"
	"phraseforge/internal/auth"
	"phraseforge/internal/bus"
	"phraseforge/internal/config"
	"phraseforge/internal/db"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/generate"
	"phraseforge/internal/ingest"
	"phraseforge/internal/jobs"
	"phraseforge/internal/llmcalls"
	"phraseforge/internal/metrics"
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

// newRouter builds the router every LLM call goes through over models, handing
// every attempt to observer. Only the server needs it: `migrate` runs before the
// ConfigMap with models.yaml exists.
func newRouter(models *modelcfg.Config, observer router.Observer) (*router.Router, error) {
	providers := map[string]router.Provider{
		"ollama":    ollama.New(),
		"openai":    openai.New(),
		"anthropic": anthropic.New(),
		"vertex":    vertex.New(),
	}
	// A provider without the credential it needs is skipped, not fatal: the key
	// may appear later (a mounted Secret), and other providers can serve.
	r, err := router.NewRouter(models, providers, router.WithLogger(slog.Default()), router.WithLogAttrs(ai.LogAttrs), router.WithObserver(observer),
		router.WithAvailability(router.CredentialAvailability(os.Getenv, os.ReadFile)))
	if err != nil {
		return nil, fmt.Errorf("models file: %w", err)
	}
	return r, nil
}

// retentionRules are the tables serve keeps from growing forever: the model-call
// history and the finished jobs, each for the configured number of days.
func retentionRules(cfg config.Config) []retention.Rule {
	const day = 24 * time.Hour
	rules := []retention.Rule{llmcalls.RetentionRule(time.Duration(cfg.LLMCallsRetentionDays) * day)}
	return append(rules, jobs.RetentionRules(
		time.Duration(cfg.JobsRetentionDaysDone)*day, time.Duration(cfg.JobsRetentionDaysFailed)*day)...)
}

func runServe(ctx context.Context, cfg config.Config) {
	// models.yaml is checked first, so a bad file is reported before anything
	// else is started; the router is built once the pool exists for its recorder.
	modelsConfig, err := config.LoadModels(cfg)
	if err != nil {
		log.Fatalf("load models: %v", err)
	}
	pool, err := db.Connect(ctx, cfg)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	llmMetrics := metrics.New()
	callStore := llmcalls.NewStore(pool)
	llmRouter, err := newRouter(modelsConfig, llmcalls.NewRecorder(modelsConfig, callStore, llmMetrics, slog.Default()))
	if err != nil {
		log.Fatalf("load models: %v", err)
	}
	llmMetrics.SetModelAvailability(func() []metrics.ModelAvailability {
		status := llmRouter.Status()
		out := make([]metrics.ModelAvailability, len(status))
		for i, s := range status {
			out[i] = metrics.ModelAvailability{Model: s.ID, Available: s.Available}
		}
		return out
	})

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
	aiSvc.SetRouter(llmRouter, modelsConfig)
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
	purger, err := retention.NewPurger(pool, retentionRules(cfg), retention.WithOnPurged(llmMetrics.Purged))
	if err != nil {
		log.Fatalf("retention rules: %v", err)
	}
	go purger.Run(ctx)

	srv := server.New(pool, authSvc, textStore, dialogStore, vocabStore, modelsStore, rolesSvc, tagsSvc, translationsSvc, aiSvc, jobsSvc)
	srv.SetIngestMaxContentBytes(cfg.IngestMaxContentBytes)
	srv.SetMetricsHandler(llmMetrics.Handler())

	busService := startBus(cfg, jobsSvc, llmMetrics)

	httpServer := &http.Server{Addr: cfg.BindAddr, Handler: srv.Router()}
	signals, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		<-signals.Done()
		// The bus first: requests already taken are answered before the process ends.
		busService.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shut down the http server: %v", err)
		}
	}()

	log.Printf("phraseforge listening on %s, routing over %d model(s)", cfg.BindAddr, len(modelsConfig.Models))
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// startBus starts answering requests over NATS when nats.url is set. A server
// that cannot be reached is a warning, not a stop: the HTTP server must run
// without the bus. The error text is not logged, to keep the warning fixed.
func startBus(cfg config.Config, store bus.JobStore, observer bus.Observer) *bus.Service {
	service, err := bus.Start(cfg.NATSURL, bus.App, store, observer, slog.Default())
	if err != nil {
		slog.Warn("the message bus is not started: phraseforge does not answer requests over NATS; check nats.url and that the NATS server is reachable")
		return nil
	}
	return service
}

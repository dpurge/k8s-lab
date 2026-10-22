package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"k8s-lab/shared/retention"
	"k8s-lab/shared/router"
	"knowledge/internal/auth"
	"knowledge/internal/chat"
	"knowledge/internal/config"
	"knowledge/internal/db"
	"knowledge/internal/embeddings"
	"knowledge/internal/generate"
	"knowledge/internal/ingest"
	"knowledge/internal/jobs"
	"knowledge/internal/llmcalls"
	"knowledge/internal/metrics"
	"knowledge/internal/modelrouter"
	"knowledge/internal/qdrant"
	"knowledge/internal/queue"
	"knowledge/internal/server"
	"knowledge/internal/translate"
)

// qdrantPromoter adapts *qdrant.Client to ingest.Promoter: the one place
// this binary hands ingest a way to reach Qdrant, keeping internal/ingest
// itself free of any qdrant import (see ingest.Promoter's doc comment).
type qdrantPromoter struct{ kb *qdrant.Client }

func (p qdrantPromoter) Promote(ctx context.Context, title, summary, body string, tags []string) (string, error) {
	it, err := p.kb.Create(ctx, title, summary, body, tags)
	return it.ID, err
}

// newEmbedder returns the embedder of cfg's embedding space: models of that space
// behind r, or a deterministic fake when embeddings.fake is set (tests and local
// runs without a model). r may be nil, for a process that embeds nothing.
func newEmbedder(cfg config.Config, r *router.Router) embeddings.Embedder {
	if cfg.Embeddings.Fake {
		return embeddings.NewFake(cfg.Embeddings.Space, cfg.Embeddings.Dimension)
	}
	return modelrouter.NewEmbedder(r, cfg.Embeddings.Space, cfg.Embeddings.Dimension)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		// migrate builds the schema and the collection from the embedding space and
		// its dimension alone: it embeds nothing, so it needs no models.yaml.
		emb := newEmbedder(cfg, nil)
		kb := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, emb, cfg.SearchMinScore)
		if err := db.Migrate(ctx, cfg); err != nil {
			log.Fatalf("migrate postgres: %v", err)
		}
		if err := kb.EnsureCollection(ctx); err != nil {
			log.Fatalf("migrate qdrant: %v", err)
		}
		log.Printf("postgres schema and collection %q ready for %s (%d dimensions)", cfg.QdrantCollection, emb.Model(), emb.Dimension())
		return
	}
	// models.yaml is read only by the server: `migrate` runs before the ConfigMap
	// with it exists. A missing or unfitting file stops the server here.
	models, err := config.LoadModels(cfg)
	if err != nil {
		log.Fatal(err)
	}
	pool, err := db.Connect(ctx, cfg)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	// Every model attempt the router makes is recorded in llm_calls and counted in
	// the metrics; recording never fails a call.
	llmMetrics := metrics.New()
	callStore := llmcalls.NewStore(pool)
	recorder := llmcalls.NewRecorder(models, callStore, llmMetrics, slog.Default())
	llmRouter, err := modelrouter.NewRouter(models, router.WithObserver(recorder))
	if err != nil {
		log.Fatal(err)
	}
	llmMetrics.SetModelAvailability(func() []metrics.ModelAvailability {
		status := llmRouter.Status()
		out := make([]metrics.ModelAvailability, len(status))
		for i, s := range status {
			out[i] = metrics.ModelAvailability{Model: s.ID, Available: s.Available}
		}
		return out
	})
	emb := newEmbedder(cfg, llmRouter)
	kb := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, emb, cfg.SearchMinScore)
	if err := kb.EnsureCollection(ctx); err != nil {
		log.Fatalf("ensure qdrant collection: %v", err)
	}

	// queueSvc is the single app-wide LLM-operation queue (see
	// specs/features/knowledge-background-queue.md): chat, generate, and
	// ingest all enqueue their LLM-calling work onto it rather than calling
	// the LLM inline, and its one worker goroutine (started below) is the
	// only thing in this process that ever makes such a call at a time.
	queueSvc := queue.New(pool)
	if err := queueSvc.FailStale(ctx); err != nil {
		log.Fatalf("fail stale operations: %v", err)
	}

	chatSvc := chat.New(pool, kb, cfg, queueSvc, modelrouter.New(llmRouter, modelrouter.KindChat, cfg.Chat))
	authSvc := auth.New(pool)
	// kb satisfies generate.ItemWriteback (SetTitle/SetSummary) directly;
	// generateSvc satisfies qdrant.BackgroundGenerator (EnqueueBackground-
	// Title/Summary) directly. Each needs the other constructed first, so
	// generateSvc is built with kb already in hand, then kb.SetGenerator
	// wires the other direction — no adapter type needed either way.
	generateSvc := generate.New(cfg, queueSvc, kb,
		modelrouter.New(llmRouter, modelrouter.KindGenerateTitle, cfg.GenerateTitle), modelrouter.New(llmRouter, modelrouter.KindGenerateSummary, cfg.GenerateSummary))
	kb.SetGenerator(generateSvc)
	jobsSvc := jobs.New(pool)
	if err := jobsSvc.FailStale(ctx); err != nil {
		log.Fatalf("fail stale jobs: %v", err)
	}
	translateSvc := translate.New(cfg, modelrouter.New(llmRouter, modelrouter.KindTranslate, cfg.Translate))
	ingestSvc := ingest.New(pool, jobsSvc, translateSvc, generateSvc, qdrantPromoter{kb: kb}, queueSvc)

	queueSvc.Register(chat.KindReply, chatSvc.HandleReply)
	queueSvc.Register(generate.KindTitle, generateSvc.HandleTitle)
	queueSvc.Register(generate.KindSummary, generateSvc.HandleSummary)
	queueSvc.Register(ingest.KindAcquire, ingestSvc.HandleAcquire)
	queueSvc.Register(ingest.KindChunk, ingestSvc.HandleChunk)
	go queueSvc.Run(ctx)

	// Model call records are kept for llm_calls.retention_days and purged in
	// batches, shortly after start and every few hours.
	purger, err := retention.NewPurger(pool,
		[]retention.Rule{llmcalls.RetentionRule(time.Duration(cfg.LLMCallsRetentionDays) * 24 * time.Hour)},
		retention.WithOnPurged(llmMetrics.Purged))
	if err != nil {
		log.Fatalf("retention rules: %v", err)
	}
	go purger.Run(ctx)

	log.Printf("knowledge listening on %s, embeddings=%s dim=%d fake=%t, routing over %d model(s) from %d provider(s)",
		cfg.BindAddr, emb.Model(), emb.Dimension(), cfg.Embeddings.Fake, len(models.Models), len(models.Providers))
	srv := server.New(kb, chatSvc, authSvc, generateSvc, jobsSvc, ingestSvc)
	srv.SetMetricsHandler(llmMetrics.Handler())
	srv.SetLLMAdmin(callStore, func() []modelrouter.ModelInfo { return modelrouter.Models(models, llmRouter) })
	log.Fatal(http.ListenAndServe(cfg.BindAddr, srv.Router()))
}

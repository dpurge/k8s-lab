package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"

	"knowledge/internal/auth"
	"knowledge/internal/chat"
	"knowledge/internal/config"
	"knowledge/internal/db"
	"knowledge/internal/embeddings"
	"knowledge/internal/generate"
	"knowledge/internal/ingest"
	"knowledge/internal/jobs"
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

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	emb, err := embeddings.New(cfg.EmbeddingsProvider, cfg.EmbeddingsBaseURL, cfg.EmbeddingsAPIKey, cfg.EmbeddingsModel, cfg.EmbeddingsDimension)
	if err != nil {
		log.Fatal(err)
	}
	kb := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, emb, cfg.SearchMinScore)
	ctx := context.Background()
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := db.Migrate(ctx, cfg); err != nil {
			log.Fatalf("migrate postgres: %v", err)
		}
		if err := kb.EnsureCollection(ctx); err != nil {
			log.Fatalf("migrate qdrant: %v", err)
		}
		log.Printf("postgres schema and collection %q ready for %s (%d dimensions)", cfg.QdrantCollection, emb.Model(), emb.Dimension())
		return
	}
	if err := kb.EnsureCollection(ctx); err != nil {
		log.Fatalf("ensure qdrant collection: %v", err)
	}
	pool, err := db.Connect(ctx, cfg)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	// queueSvc is the single app-wide LLM-operation queue (see
	// specs/features/knowledge-background-queue.md): chat, generate, and
	// ingest all enqueue their LLM-calling work onto it rather than calling
	// the LLM inline, and its one worker goroutine (started below) is the
	// only thing in this process that ever makes such a call at a time.
	queueSvc := queue.New(pool)
	if err := queueSvc.FailStale(ctx); err != nil {
		log.Fatalf("fail stale operations: %v", err)
	}

	chatSvc := chat.New(pool, kb, cfg, queueSvc)
	authSvc := auth.New(pool)
	// kb satisfies generate.ItemWriteback (SetTitle/SetSummary) directly;
	// generateSvc satisfies qdrant.BackgroundGenerator (EnqueueBackground-
	// Title/Summary) directly. Each needs the other constructed first, so
	// generateSvc is built with kb already in hand, then kb.SetGenerator
	// wires the other direction — no adapter type needed either way.
	generateSvc := generate.New(cfg, queueSvc, kb)
	kb.SetGenerator(generateSvc)
	jobsSvc := jobs.New(pool)
	if err := jobsSvc.FailStale(ctx); err != nil {
		log.Fatalf("fail stale jobs: %v", err)
	}
	translateSvc := translate.New(cfg)
	ingestSvc := ingest.New(pool, jobsSvc, translateSvc, generateSvc, qdrantPromoter{kb: kb}, queueSvc)

	queueSvc.Register(chat.KindReply, chatSvc.HandleReply)
	queueSvc.Register(generate.KindTitle, generateSvc.HandleTitle)
	queueSvc.Register(generate.KindSummary, generateSvc.HandleSummary)
	queueSvc.Register(ingest.KindAcquire, ingestSvc.HandleAcquire)
	queueSvc.Register(ingest.KindChunk, ingestSvc.HandleChunk)
	go queueSvc.Run(ctx)

	log.Printf("knowledge listening on %s, embeddings=%s/%s dim=%d chat=%s/%s generate=%s/%s", cfg.BindAddr, cfg.EmbeddingsProvider, emb.Model(), emb.Dimension(), cfg.ChatProvider, cfg.ChatModel, cfg.GenerateProvider, cfg.GenerateModel)
	log.Fatal(http.ListenAndServe(cfg.BindAddr, server.New(kb, chatSvc, authSvc, generateSvc, jobsSvc, ingestSvc).Router()))
}

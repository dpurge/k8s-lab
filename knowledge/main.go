package main

import (
	"context"
	"log"
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
	chatSvc := chat.New(pool, kb, cfg)
	authSvc := auth.New(pool)
	generateSvc := generate.New(cfg)
	jobsSvc := jobs.New(pool)
	if err := jobsSvc.FailStale(ctx); err != nil {
		log.Fatalf("fail stale jobs: %v", err)
	}
	translateSvc := translate.New(cfg)
	ingestSvc := ingest.New(pool, jobsSvc, translateSvc, generateSvc, qdrantPromoter{kb: kb})
	log.Printf("knowledge listening on %s, embeddings=%s/%s dim=%d chat=%s/%s generate=%s/%s", cfg.BindAddr, cfg.EmbeddingsProvider, emb.Model(), emb.Dimension(), cfg.ChatProvider, cfg.ChatModel, cfg.GenerateProvider, cfg.GenerateModel)
	log.Fatal(http.ListenAndServe(cfg.BindAddr, server.New(kb, chatSvc, authSvc, generateSvc, jobsSvc, ingestSvc).Router()))
}

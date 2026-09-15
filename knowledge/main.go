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
	"knowledge/internal/qdrant"
	"knowledge/internal/server"
)

func main() {
	cfg := config.Load()
	emb, err := embeddings.New(cfg.EmbeddingsProvider, cfg.EmbeddingsBaseURL, cfg.EmbeddingsAPIKey, cfg.EmbeddingsModel, cfg.EmbeddingsDimension)
	if err != nil {
		log.Fatal(err)
	}
	kb := qdrant.New(cfg.QdrantURL, cfg.QdrantCollection, emb)
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
	log.Printf("knowledge listening on %s, embeddings=%s/%s dim=%d chat=%s/%s", cfg.BindAddr, cfg.EmbeddingsProvider, emb.Model(), emb.Dimension(), cfg.ChatProvider, cfg.ChatModel)
	log.Fatal(http.ListenAndServe(cfg.BindAddr, server.New(kb, chatSvc, authSvc).Router()))
}

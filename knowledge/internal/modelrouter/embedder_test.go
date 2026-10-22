package modelrouter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

const embeddingModels = `
providers:
  - id: lan
    type: ollama
    base_url: http://gpu.lan:11434
    api_key_from: ""
  - id: local
    type: ollama
    base_url: http://localhost:11434
    api_key_from: ""
models:
  - id: bge-lan
    provider: lan
    model: bge-m3
    tags: [embed]
    priority: 100
    embedding_space: bge-m3
    dimension: 3
  - id: bge-local
    provider: local
    model: bge-m3
    tags: [embed]
    priority: 10
    embedding_space: bge-m3
    dimension: 3
  - id: nomic-local
    provider: local
    model: nomic-embed-text
    tags: [embed]
    priority: 50
    embedding_space: nomic
    dimension: 3
`

func embedderOver(t *testing.T, scripts map[string][]routertest.Result) (*Embedder, *routertest.Provider) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(embeddingModels), 0o644); err != nil {
		t.Fatal(err)
	}
	models, err := modelcfg.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := routertest.NewProvider(scripts)
	r, err := router.NewRouter(models, map[string]router.Provider{"ollama": fake})
	if err != nil {
		t.Fatal(err)
	}
	return NewEmbedder(r, "bge-m3", 3), fake
}

func TestEmbedderReportsItsSpaceAndDimensionAndReturnsTheVector(t *testing.T) {
	e, fake := embedderOver(t, map[string][]routertest.Result{"bge-lan": {routertest.Embedding(3, 2)}})
	if e.Model() != "bge-m3" || e.Dimension() != 3 {
		t.Errorf("Model() = %q, Dimension() = %d; want the space name and 3", e.Model(), e.Dimension())
	}
	v, err := e.Embed(context.Background(), "some text")
	if err != nil || len(v) != 3 || v[0] != 1 || v[2] != 3 {
		t.Fatalf("Embed() = %v, %v; want the 3-number vector as float64", v, err)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0].EmbedRequest.Space != "bge-m3" || calls[0].EmbedRequest.Input != "some text" {
		t.Errorf("calls = %+v, want one embedding call for the space with the text", calls)
	}
}

func TestEmbedderFallsBackToAnotherCopyOfTheModelButNeverAnotherSpace(t *testing.T) {
	e, fake := embedderOver(t, map[string][]routertest.Result{
		"bge-lan":     {routertest.Fail(router.FailServer, "lan down")},
		"bge-local":   {routertest.Embedding(3, 1)},
		"nomic-local": {routertest.Embedding(3, 1)},
	})
	if _, err := e.Embed(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.Models(), ","); got != "bge-lan,bge-local" {
		t.Errorf("models called = %s, want bge-lan then bge-local, never nomic-local (priority 50, another space)", got)
	}
}

func TestEmbedderFailsWhenTheWholeSpaceFailsAndNeverUsesAnotherSpace(t *testing.T) {
	e, fake := embedderOver(t, map[string][]routertest.Result{
		"bge-lan":   {routertest.Fail(router.FailServer, "lan down")},
		"bge-local": {routertest.Fail(router.FailServer, "local down")},
	})
	_, err := e.Embed(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "bge-lan") || !strings.Contains(err.Error(), "bge-local") || !strings.Contains(err.Error(), "no model of another embedding space is tried") {
		t.Errorf("err = %v, want both models listed and the note about other spaces", err)
	}
	for _, id := range fake.Models() {
		if id == "nomic-local" {
			t.Error("the model of another space was called")
		}
	}
}

func TestEmbedderWithoutARouterOnlyKnowsItsSpace(t *testing.T) {
	e := NewEmbedder(nil, "bge-m3", 1024)
	if e.Model() != "bge-m3" || e.Dimension() != 1024 {
		t.Errorf("Model() = %q, Dimension() = %d", e.Model(), e.Dimension())
	}
	if _, err := e.Embed(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "no model router") {
		t.Errorf("err = %v, want a clear error without a router", err)
	}
}

func TestEmbedderStopsOnACancelledContext(t *testing.T) {
	e, _ := embedderOver(t, map[string][]routertest.Result{"bge-lan": {{Block: true}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Embed(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

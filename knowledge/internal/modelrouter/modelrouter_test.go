package modelrouter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
	"knowledge/internal/config"
)

const twoModels = `
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
  - id: big
    provider: lan
    model: gemma4:31b
    tags: [default, chat]
    priority: 100
    options: { num_ctx: 8192 }
  - id: small
    provider: local
    model: gemma4:12b
    tags: [default, chat]
    priority: 10
    options: { num_ctx: 8192 }
  - id: titles-only
    provider: local
    model: gemma4:12b
    tags: [default]
    priority: 200
    options: { num_ctx: 8192 }
`

func newRouterOver(t *testing.T, yaml string, scripts map[string][]routertest.Result) (*router.Router, *routertest.Provider) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
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
	return r, fake
}

func TestCompleteSendsThePurposesTagsWindowAndMessages(t *testing.T) {
	r, fake := newRouterOver(t, twoModels, map[string][]routertest.Result{"big": {routertest.OK("hello", 3, 1)}})
	p := New(r, KindChat, config.PurposeConfig{ModelTags: []string{"chat"}, NumCtx: 4096, TimeoutSeconds: 30})
	msgs := []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}}
	got, err := p.Complete(context.Background(), msgs)
	if err != nil || got != "hello" {
		t.Fatalf("Complete() = %q, %v; want hello, nil", got, err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].ModelID != "big" {
		t.Fatalf("calls = %+v, want one call to the highest-priority model with the chat tag", calls)
	}
	req := calls[0].Request
	if req.NumCtx != 4096 || req.Timeout != 30*time.Second || len(req.Messages) != 2 || req.Messages[1].Content != "hi" {
		t.Errorf("request = %+v, want the purpose's window and timeout and both messages", req)
	}
}

func TestCompleteOnlyUsesModelsWithAllTheTags(t *testing.T) {
	// titles-only has the highest priority but lacks the chat tag.
	r, fake := newRouterOver(t, twoModels, nil)
	p := New(r, KindChat, config.PurposeConfig{ModelTags: []string{"chat"}})
	if _, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Models(); len(got) != 1 || got[0] != "big" {
		t.Errorf("models called = %v, want only big", got)
	}
}

func TestCompleteFallsBackToTheNextModelWhenOneFails(t *testing.T) {
	r, fake := newRouterOver(t, twoModels, map[string][]routertest.Result{
		"big":   {routertest.Fail(router.FailServer, "boom")},
		"small": {routertest.OK("from small", 1, 1)},
	})
	p := New(r, KindChat, config.PurposeConfig{ModelTags: []string{"chat"}})
	got, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "x"}})
	if err != nil || got != "from small" {
		t.Fatalf("Complete() = %q, %v; want the fallback model's reply", got, err)
	}
	if models := fake.Models(); len(models) != 2 || models[0] != "big" || models[1] != "small" {
		t.Errorf("models called = %v, want big then small", models)
	}
}

func TestCompleteErrorNamesEveryModelWhenAllFail(t *testing.T) {
	r, _ := newRouterOver(t, twoModels, map[string][]routertest.Result{
		"big":   {routertest.Fail(router.FailServer, "lan down")},
		"small": {routertest.Fail(router.FailServer, "local down")},
	})
	p := New(r, KindChat, config.PurposeConfig{ModelTags: []string{"chat"}})
	_, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "x"}})
	if err == nil || !strings.Contains(err.Error(), "big") || !strings.Contains(err.Error(), "small") || !strings.Contains(err.Error(), "lan down") {
		t.Errorf("err = %v, want it to list both models and why each failed", err)
	}
}

func TestCompleteStopsAtOnceOnACancelledContext(t *testing.T) {
	r, fake := newRouterOver(t, twoModels, map[string][]routertest.Result{"big": {{Block: true}}})
	p := New(r, KindChat, config.PurposeConfig{ModelTags: []string{"chat"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Complete(ctx, []Message{{Role: "user", Content: "x"}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(fake.Models()) > 1 {
		t.Errorf("models called = %v, want the chain to stop at the first", fake.Models())
	}
}

func TestCompleteFailsClearlyWhenNoModelHasTheTags(t *testing.T) {
	r, _ := newRouterOver(t, twoModels, nil)
	p := New(r, KindChat, config.PurposeConfig{ModelTags: []string{"review"}})
	_, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "x"}})
	if err == nil || !strings.Contains(err.Error(), "review") {
		t.Errorf("err = %v, want it to name the missing tag", err)
	}
}

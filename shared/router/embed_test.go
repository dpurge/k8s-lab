package router_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

// Two copies of bge-m3 (a GPU box that is usually faster, and the laptop), and
// a model of another space with the same length, which must never stand in.
const embedConfig = `providers:
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
  - {id: gpu-box, type: ollama, base_url: "http://gpu-box.lan:11434", api_key_from: ""}
models:
  - {id: chat-gpu, provider: gpu-box, model: gemma4:31b, tags: [embed, chat], priority: 90}
  - {id: bge-gpu, provider: gpu-box, model: bge-m3, tags: [embed], priority: 100, embedding_space: bge-m3, dimension: 4}
  - {id: bge-laptop, provider: laptop, model: bge-m3, tags: [embed], priority: 10, embedding_space: bge-m3, dimension: 4}
  - {id: other-space, provider: laptop, model: nomic-embed-text, tags: [embed], priority: 50, embedding_space: nomic, dimension: 4}
routing:
  breaker: {failures: 3, cooldown_seconds: 60, rate_limited_cooldown_seconds: 30}
`

func (r *rig) embed(space string) (router.EmbedResponse, error) {
	return r.router.Embed(context.Background(), router.EmbedRequest{Space: space, Input: "some text"})
}

func TestEmbedUsesTheBestModelOfTheSpaceAndNoOtherSpace(t *testing.T) {
	r := newRig(t, embedConfig, map[string][]routertest.Result{"bge-gpu": {routertest.Embedding(4, 3)}})
	resp, err := r.embed("bge-m3")
	if err != nil {
		t.Fatal(err)
	}
	if resp.ModelID != "bge-gpu" || len(resp.Vector) != 4 || resp.InputTokens != 3 {
		t.Errorf("resp = %+v, want bge-gpu (priority 100, not the 50 of another space), 4 numbers, 3 tokens", resp)
	}
	calls := r.provider.Calls()
	if len(calls) != 1 || calls[0].EmbedRequest.Input != "some text" || calls[0].EmbedRequest.Space != "bge-m3" {
		t.Errorf("calls = %+v, want one embedding call with the text and the space", calls)
	}
}

func TestEmbedFallsBackWithinTheSpaceOnly(t *testing.T) {
	r := newRig(t, embedConfig, map[string][]routertest.Result{
		"bge-gpu":    {routertest.Fail(router.FailServer, "gpu box is down")},
		"bge-laptop": {routertest.Embedding(4, 1)},
	})
	resp, err := r.embed("bge-m3")
	if err != nil || resp.ModelID != "bge-laptop" {
		t.Fatalf("resp = %+v, err = %v; want the laptop copy of the same model", resp, err)
	}
	if got := strings.Join(r.provider.Models(), ","); got != "bge-gpu,bge-laptop" {
		t.Errorf("models called = %s, want bge-gpu then bge-laptop and never other-space or chat-gpu", got)
	}
}

func TestEmbedNeverTriesAnotherSpaceWhenTheWholeSpaceFails(t *testing.T) {
	r := newRig(t, embedConfig, map[string][]routertest.Result{
		"bge-gpu":     {routertest.Fail(router.FailServer, "gpu down")},
		"bge-laptop":  {routertest.Fail(router.FailServer, "laptop down")},
		"other-space": {routertest.Embedding(4, 1)},
	})
	_, err := r.embed("bge-m3")
	var all *router.AllFailedError
	if !errors.As(err, &all) || len(all.Attempts) != 2 {
		t.Fatalf("err = %v, want both models of the space listed as failed", err)
	}
	if !strings.Contains(err.Error(), "no model of another embedding space is tried") || !strings.Contains(err.Error(), "gpu down") {
		t.Errorf("err = %v, want the reasons and the note that no other space is used", err)
	}
	for _, id := range r.provider.Models() {
		if id == "other-space" || id == "chat-gpu" {
			t.Errorf("called %s, which is not in the requested space", id)
		}
	}
}

func TestEmbedFailsClearlyForASpaceNoModelHas(t *testing.T) {
	r := newRig(t, embedConfig, nil)
	_, err := r.embed("unknown-space")
	var none *router.NoModelError
	if !errors.As(err, &none) || !strings.Contains(err.Error(), `unknown-space`) || !strings.Contains(err.Error(), "no model of another embedding space is tried") {
		t.Errorf("err = %v, want a no-model error naming the space", err)
	}
	if _, err := r.router.Embed(context.Background(), router.EmbedRequest{Input: "x"}); err == nil {
		t.Error("an embedding call without a space must be refused")
	}
}

func TestAVectorOfTheWrongLengthIsAFailedAttemptNeverReturned(t *testing.T) {
	r := newRig(t, embedConfig, map[string][]routertest.Result{
		"bge-gpu":    {routertest.Embedding(3, 1)}, // dimension is 4
		"bge-laptop": {routertest.Embedding(4, 1)},
	})
	resp, err := r.embed("bge-m3")
	if err != nil || resp.ModelID != "bge-laptop" || len(resp.Vector) != 4 {
		t.Fatalf("resp = %+v, err = %v; want the next model's correct vector", resp, err)
	}
	attempts := r.observer.Attempts()
	if len(attempts) != 2 || attempts[0].Outcome != router.OutcomeError || !strings.Contains(attempts[0].Error, "3 numbers, but its dimension is 4") {
		t.Errorf("attempts = %+v, want the first recorded as an error naming both lengths", attempts)
	}
	// When no model of the space has the right length, the call fails and returns nothing.
	r2 := newRig(t, embedConfig, map[string][]routertest.Result{"bge-gpu": {routertest.Embedding(5, 1)}, "bge-laptop": {routertest.Embedding(5, 1)}})
	resp, err = r2.embed("bge-m3")
	if err == nil || len(resp.Vector) != 0 {
		t.Errorf("resp = %+v, err = %v; want a failure and no vector", resp, err)
	}
}

func TestEmbedAttemptsAreMarkedAndCarryTokens(t *testing.T) {
	r := newRig(t, embedConfig, map[string][]routertest.Result{"bge-gpu": {routertest.Embedding(4, 7)}})
	if _, err := r.embed("bge-m3"); err != nil {
		t.Fatal(err)
	}
	a := r.observer.Attempts()
	if len(a) != 1 || a[0].Kind != router.AttemptEmbed || a[0].ModelID != "bge-gpu" || a[0].Outcome != router.OutcomeOK || a[0].InputTokens != 7 || a[0].OutputTokens != 0 {
		t.Errorf("attempts = %+v, want one ok embed attempt with 7 input tokens", a)
	}
	// A chat call leaves a normal attempt with an empty kind.
	r2 := newRig(t, embedConfig, nil)
	if _, err := r2.call("chat"); err != nil {
		t.Fatal(err)
	}
	if got := r2.observer.Attempts(); len(got) != 1 || got[0].Kind != "" {
		t.Errorf("chat attempts = %+v, want an empty kind", got)
	}
}

func TestAChatCallNeverGoesToAnEmbeddingModel(t *testing.T) {
	// bge-gpu has the highest priority and the tag embed, but is an embedding model.
	r := newRig(t, embedConfig, nil)
	resp, err := r.call("embed")
	if err != nil || resp.ModelID != "chat-gpu" {
		t.Fatalf("resp = %+v, err = %v; want chat-gpu, the only non-embedding model tagged embed", resp, err)
	}
	if _, err := r.call(); err != nil {
		t.Fatal(err)
	}
	if got := r.provider.Models(); got[len(got)-1] != "chat-gpu" {
		t.Errorf("a call without tags went to %v, want chat-gpu", got)
	}
	sel, err := r.router.Select([]string{"embed"})
	if err != nil || ids(sel) != "chat-gpu" {
		t.Errorf("Select(embed) = %s, %v; want chat-gpu only", ids(sel), err)
	}
}

func TestNewRouterRefusesAnEmbeddingModelOnAProviderThatCannotEmbed(t *testing.T) {
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(embedConfig))
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.NewRouter(cfg, map[string]router.Provider{"ollama": chatOnly{}})
	if err == nil || !strings.Contains(err.Error(), "is an embedding model, but its provider type \"ollama\" has no embeddings implementation") {
		t.Errorf("err = %v, want the embedding model refused with a clear message", err)
	}
}

type chatOnly struct{}

func (chatOnly) Complete(context.Context, modelcfg.Provider, modelcfg.Model, router.Request) (router.Response, error) {
	return router.Response{}, nil
}

func TestEmbedStopsAtOnceOnACanceledContext(t *testing.T) {
	r := newRig(t, embedConfig, map[string][]routertest.Result{"bge-gpu": {{Block: true}}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := r.router.Embed(ctx, router.EmbedRequest{Space: "bge-m3", Input: "x"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's error", err)
	}
	if got := r.provider.Models(); len(got) != 1 {
		t.Errorf("models called = %v, want the chain to stop at the first", got)
	}
}

func TestEmbedFeedsTheSameBreakerAsChat(t *testing.T) {
	r := newRig(t, embedConfig, map[string][]routertest.Result{
		"bge-gpu":    {routertest.Fail(router.FailServer, "boom")},
		"bge-laptop": {routertest.Embedding(4, 1)},
	})
	for i := 0; i < 3; i++ {
		if _, err := r.embed("bge-m3"); err != nil {
			t.Fatal(err)
		}
	}
	// Three server failures in a row hold the model out: the fourth call skips it.
	before := len(r.provider.Models())
	if _, err := r.embed("bge-m3"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.provider.Models()[before:], ","); got != "bge-laptop" {
		t.Errorf("models called = %s, want only bge-laptop while bge-gpu is held out", got)
	}
}

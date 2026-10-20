package llm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"assistant/internal/callctx"
	"assistant/internal/llm"
	"assistant/internal/llm/llmtest"
	"assistant/internal/workflow"
	"k8s-lab/shared/modelcfg"
)

const config = `providers:
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
models:
  - {id: laptop-a, provider: laptop, model: gemma4:12b, tags: [chat], priority: 10}
`

var t0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

type rig struct {
	router   *llm.Router
	provider *llmtest.Provider
	observer *llmtest.Observer
}

func newRig(t *testing.T, opts ...llm.Option) *rig {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(config))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{provider: llmtest.NewProvider(nil), observer: &llmtest.Observer{}}
	opts = append(opts, llm.WithClock(llmtest.NewClock(t0)), llm.WithObserver(r.observer))
	r.router, err = llm.NewRouter(cfg, map[string]llm.Provider{"ollama": r.provider}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAttributionRequiredRefusesACallWithoutAUserAndCallsNobody(t *testing.T) {
	r := newRig(t, llm.RequireAttribution())
	for name, ctx := range map[string]context.Context{
		"plain context": context.Background(),
		"task only":     callctx.With(context.Background(), callctx.Info{TaskID: "t"}),
	} {
		_, err := r.router.Complete(ctx, workflow.Request{Tags: []string{"chat"}})
		if !errors.Is(err, llm.ErrNoAttribution) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(r.provider.Calls()) != 0 || len(r.observer.Attempts()) != 0 {
		t.Errorf("nothing may be called or recorded for a call nobody owns")
	}
}

func TestAttributionRequiredAcceptsACallThatHasAUser(t *testing.T) {
	r := newRig(t, llm.RequireAttribution())
	ctx := callctx.With(context.Background(), callctx.Info{TaskID: "t", UserID: "u"})
	if _, err := r.router.Complete(ctx, workflow.Request{Tags: []string{"chat"}}); err != nil {
		t.Fatal(err)
	}
	if len(r.provider.Calls()) != 1 {
		t.Errorf("calls = %d", len(r.provider.Calls()))
	}
}

func TestAttributionIsNotRequiredUnlessAskedFor(t *testing.T) {
	r := newRig(t)
	if _, err := r.router.Complete(context.Background(), workflow.Request{Tags: []string{"chat"}}); err != nil {
		t.Errorf("without the option a bare context is fine: %v", err)
	}
}

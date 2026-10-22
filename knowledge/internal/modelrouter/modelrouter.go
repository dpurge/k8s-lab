// Package modelrouter connects knowledge's LLM purposes (chat, title, summary,
// translate) to the shared model router: each purpose asks for models by tag and
// the router picks the highest-priority available one, falling back to the next.
package modelrouter

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/anthropic"
	"k8s-lab/shared/router/ollama"
	"k8s-lab/shared/router/openai"
	"k8s-lab/shared/router/vertex"
	"knowledge/internal/config"
)

// Message is one turn of a conversation: a role (system, user, assistant) and
// its text.
type Message = router.Message

// Completer makes one model call for one purpose and returns the reply text.
// The services take this interface, so tests give them a scripted one.
type Completer interface {
	Complete(ctx context.Context, messages []Message) (string, error)
}

// Purpose is a router bound to one purpose's tags, context window and timeout.
type Purpose struct {
	router  *router.Router
	kind    string
	tags    []string
	numCtx  int
	timeout time.Duration
}

// New binds r to the purpose p, whose calls are of the given kind (KindChat and
// the like) in records and logs.
func New(r *router.Router, kind string, p config.PurposeConfig) Purpose {
	return Purpose{
		router:  r,
		kind:    kind,
		tags:    p.ModelTags,
		numCtx:  p.NumCtx,
		timeout: time.Duration(p.TimeoutSeconds) * time.Second,
	}
}

// Complete sends messages to the best available model with the purpose's tags.
// When every model fails the error lists each one and why; a cancelled context
// stops at once.
func (p Purpose) Complete(ctx context.Context, messages []Message) (string, error) {
	resp, err := p.router.Complete(WithKind(ctx, p.kind), router.Request{
		Messages: messages,
		Tags:     p.tags,
		NumCtx:   p.numCtx,
		Timeout:  p.timeout,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// NewRouter builds the router every model call goes through over models, with
// all provider types. A provider without the credential it needs is skipped,
// not fatal: the credential may appear later (a mounted Secret), and other
// providers can serve. More options (an observer) can be added by the caller.
func NewRouter(models *modelcfg.Config, opts ...router.Option) (*router.Router, error) {
	providers := map[string]router.Provider{
		"ollama":    ollama.New(),
		"openai":    openai.New(),
		"anthropic": anthropic.New(),
		"vertex":    vertex.New(),
	}
	opts = append([]router.Option{
		router.WithLogger(slog.Default()),
		router.WithLogAttrs(LogAttrs),
		router.WithAvailability(router.CredentialAvailability(os.Getenv, os.ReadFile)),
	}, opts...)
	r, err := router.NewRouter(models, providers, opts...)
	if err != nil {
		return nil, fmt.Errorf("models file: %w", err)
	}
	return r, nil
}

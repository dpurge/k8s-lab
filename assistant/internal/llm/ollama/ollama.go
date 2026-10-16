// Package ollama is the router's provider for Ollama, on this machine or any
// other in the network. It is a thin layer over the shared chat client, which
// already handles Ollama's streaming, its timeouts and its token counts; this
// package maps a step request onto it and classifies how a call failed.
package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	sharedllm "k8s-lab/shared/llm"

	"assistant/internal/llm"
	"assistant/internal/models"
	"assistant/internal/workflow"
)

// Provider calls Ollama.
type Provider struct {
	// unit is what one "second" of a timeout option lasts; tests shorten it.
	unit time.Duration
}

var _ llm.Provider = (*Provider)(nil)

// Option configures a Provider.
type Option func(*Provider)

// WithTimeoutUnit changes the length of the "second" in the timeout options, so
// that a test can exercise a timeout without waiting for it.
func WithTimeoutUnit(d time.Duration) Option { return func(p *Provider) { p.unit = d } }

// New returns an Ollama provider.
func New(opts ...Option) *Provider {
	p := &Provider{unit: time.Second}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Complete sends the request's system prompt (when there is one) and input as messages, asks
// Ollama to constrain the reply to the request's schema, and returns the text
// with the token counts Ollama reports. Every error is an *llm.Failure.
func (p *Provider) Complete(ctx context.Context, prov models.Provider, m models.Model, req workflow.Request) (workflow.Response, error) {
	cfg := sharedllm.Config{
		Provider:          "ollama",
		BaseURL:           prov.BaseURL,
		Model:             m.Name,
		NumCtx:            m.Options.NumCtx,
		NumPredict:        m.Options.NumPredict,
		Think:             m.Options.Think,
		Timeout:           time.Duration(m.Options.OverallTimeoutSeconds) * p.unit,
		FirstTokenTimeout: time.Duration(m.Options.FirstTokenTimeoutSeconds) * p.unit,
		IdleTimeout:       time.Duration(m.Options.IdleTimeoutSeconds) * p.unit,
	}
	if len(req.Schema) > 0 {
		format, err := formatOf(req.Schema)
		if err != nil {
			return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: err}
		}
		cfg.Format = format
	}

	messages := []sharedllm.Message{{Role: "user", Content: req.Input}}
	if req.System != "" {
		messages = append([]sharedllm.Message{{Role: "system", Content: req.System}}, messages...)
	}
	reply, err := sharedllm.New(cfg).Chat(ctx, messages, nil)
	if err != nil {
		return workflow.Response{}, classify(err)
	}
	return workflow.Response{Text: reply.Content, InputTokens: reply.PromptTokens, OutputTokens: reply.ReplyTokens}, nil
}

// formatOf is the schema as Ollama's "format". The $schema declaration says
// nothing to the model server, so it is left out.
func formatOf(schema map[string]any) (json.RawMessage, error) {
	trimmed := make(map[string]any, len(schema))
	for k, v := range schema {
		if k != "$schema" {
			trimmed[k] = v
		}
	}
	b, err := json.Marshal(trimmed)
	if err != nil {
		return nil, fmt.Errorf("encode the reply schema: %w", err)
	}
	return b, nil
}

// classify says how a call failed, in the terms the router acts on.
//   - the caller's own context ending: canceled;
//   - one of the client's limits (first token, idle, overall): timeout;
//   - a status reply: 429 rate limited, 5xx server, other 4xx client;
//   - a stream that broke or carried an error: server;
//   - failing to open a connection at all (refused, no route, DNS, dial
//     timeout): connection, a fault of the machine rather than of a model;
//   - anything else: server, so that a model that keeps misbehaving is set aside.
func classify(err error) *llm.Failure {
	var timeout *sharedllm.TimeoutError
	var status *sharedllm.StatusError
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case errors.As(err, &timeout):
		return &llm.Failure{Kind: llm.FailTimeout, Err: err}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &llm.Failure{Kind: llm.FailCanceled, Err: err}
	case errors.As(err, &status):
		switch {
		case status.StatusCode == 429:
			return &llm.Failure{Kind: llm.FailRateLimited, Err: err}
		case status.StatusCode >= 500:
			return &llm.Failure{Kind: llm.FailServer, Err: err}
		}
		return &llm.Failure{Kind: llm.FailClient, Err: err}
	case errors.Is(err, sharedllm.ErrStreamIncomplete), errors.Is(err, io.ErrUnexpectedEOF):
		return &llm.Failure{Kind: llm.FailServer, Err: err}
	case errors.As(err, &dns):
		return &llm.Failure{Kind: llm.FailConnection, Err: err}
	case errors.As(err, &op) && op.Op == "dial":
		return &llm.Failure{Kind: llm.FailConnection, Err: err}
	}
	return &llm.Failure{Kind: llm.FailServer, Err: err}
}

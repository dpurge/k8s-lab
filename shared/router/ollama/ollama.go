// Package ollama is the router's provider for Ollama, on this machine or any
// other in the network. It is a thin layer over the shared chat client, which
// already handles Ollama's streaming, its timeouts and its token counts; this
// package maps a step request onto it and classifies how a call failed.
//
// When the provider's api_key_from is env:NAME or file:/path, the key is read from that
// environment variable or file on every call (modelcfg.ResolveCredential; a file
// is re-read, an environment variable of a running process does not change) and
// sent as a Bearer key (for an Ollama behind an authenticating proxy); an
// empty api_key_from sends no Authorization header. The key is never logged
// or put in an error.
//
// The provider's headers (modelcfg.ResolveHeaders) go to the client as
// Config.Headers and are sent on every request.
//
// The request's NumCtx, MaxTokens, Think and Timeout, when set, replace the
// model's num_ctx, num_predict, think and overall_timeout_seconds. The first-token and idle
// timeouts stay the model's.
package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	sharedllm "k8s-lab/shared/llm"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

// Provider calls Ollama.
type Provider struct {
	getenv   func(string) string
	readFile func(string) ([]byte, error)
	// unit is what one "second" of a timeout option lasts; tests shorten it.
	unit time.Duration
}

var _ router.Provider = (*Provider)(nil)

// Option configures a Provider.
type Option func(*Provider)

// WithGetenv replaces how the API key is looked up by variable name; tests use
// it instead of the process environment.
func WithGetenv(f func(string) string) Option { return func(p *Provider) { p.getenv = f } }

// WithReadFile replaces how an api_key_from file: is read; tests use it instead of the
// file system.
func WithReadFile(f func(string) ([]byte, error)) Option { return func(p *Provider) { p.readFile = f } }

// WithTimeoutUnit changes the length of the "second" in the timeout options, so
// that a test can exercise a timeout without waiting for it.
func WithTimeoutUnit(d time.Duration) Option { return func(p *Provider) { p.unit = d } }

// New returns an Ollama provider.
func New(opts ...Option) *Provider {
	p := &Provider{getenv: os.Getenv, readFile: os.ReadFile, unit: time.Second}
	for _, o := range opts {
		o(p)
	}
	return p
}

// key looks the API key up. It is never logged or put in an error.
func (p *Provider) key(prov modelcfg.Provider) (string, error) {
	cred := modelcfg.ResolveCredential(prov, p.getenv, p.readFile)
	if !cred.Configured {
		return "", nil
	}
	return cred.Require(prov.ID)
}

// Complete sends the request's system prompt (when there is one) and input as messages, asks
// Ollama to constrain the reply to the request's schema, and returns the text
// with the token counts Ollama reports. Every error is an *router.Failure.
func (p *Provider) Complete(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model, req router.Request) (router.Response, error) {
	if err := req.Validate(); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("ollama: invalid request: %w", err)}
	}
	key, err := p.key(prov)
	if err != nil {
		// A configuration problem, not the model's: it must not trip the breaker.
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("ollama: %w", err)}
	}
	headers := modelcfg.ResolveHeaders(prov, p.getenv, p.readFile)
	if err := headers.Require(prov.ID); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("ollama: %w", err)}
	}
	cfg := sharedllm.Config{
		Provider:          "ollama",
		BaseURL:           prov.BaseURL,
		APIKey:            key,
		Headers:           headers.Map(),
		Model:             m.Name,
		NumCtx:            m.Options.NumCtx,
		NumPredict:        m.Options.NumPredict,
		Think:             m.Options.Think,
		Timeout:           time.Duration(m.Options.OverallTimeoutSeconds) * p.unit,
		FirstTokenTimeout: time.Duration(m.Options.FirstTokenTimeoutSeconds) * p.unit,
		IdleTimeout:       time.Duration(m.Options.IdleTimeoutSeconds) * p.unit,
	}
	if req.NumCtx > 0 {
		cfg.NumCtx = req.NumCtx
	}
	if req.MaxTokens > 0 {
		cfg.NumPredict = req.MaxTokens
	}
	if req.Think != nil {
		cfg.Think = *req.Think
	}
	if req.Timeout > 0 {
		cfg.Timeout = req.Timeout
	}
	if len(req.Schema) > 0 {
		format, err := formatOf(req.Schema)
		if err != nil {
			return router.Response{}, &router.Failure{Kind: router.FailClient, Err: err}
		}
		cfg.Format = format
	}

	system, turns := req.Conversation()
	var messages []sharedllm.Message
	if system != "" {
		messages = append(messages, sharedllm.Message{Role: "system", Content: system})
	}
	for _, t := range turns {
		messages = append(messages, sharedllm.Message{Role: t.Role, Content: t.Content})
	}
	reply, err := sharedllm.New(cfg).Chat(ctx, messages, nil)
	if err != nil {
		return router.Response{}, classify(err)
	}
	return router.Response{Text: reply.Content, InputTokens: reply.PromptTokens, OutputTokens: reply.ReplyTokens}, nil
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
func classify(err error) *router.Failure {
	var timeout *sharedllm.TimeoutError
	var status *sharedllm.StatusError
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case errors.As(err, &timeout):
		return &router.Failure{Kind: router.FailTimeout, Err: err}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &router.Failure{Kind: router.FailCanceled, Err: err}
	case errors.As(err, &status):
		switch {
		case status.StatusCode == 429:
			return &router.Failure{Kind: router.FailRateLimited, Err: err, HTTPStatus: status.StatusCode}
		case status.StatusCode >= 500:
			return &router.Failure{Kind: router.FailServer, Err: err, HTTPStatus: status.StatusCode}
		}
		return &router.Failure{Kind: router.FailClient, Err: err, HTTPStatus: status.StatusCode}
	case errors.Is(err, sharedllm.ErrStreamIncomplete), errors.Is(err, io.ErrUnexpectedEOF):
		return &router.Failure{Kind: router.FailServer, Err: err}
	case errors.As(err, &dns):
		return &router.Failure{Kind: router.FailConnection, Err: err}
	case errors.As(err, &op) && op.Op == "dial":
		return &router.Failure{Kind: router.FailConnection, Err: err}
	}
	return &router.Failure{Kind: router.FailServer, Err: err}
}

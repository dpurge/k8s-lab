// Package openai is the router's provider for OpenAI-compatible chat APIs
// (OpenAI, OpenRouter, or a local server that speaks the same protocol),
// through POST {base_url}/chat/completions.
//
// The provider's headers (modelcfg.ResolveHeaders) are set after its own.
//
// The API key is read on every call (a file: is re-read; an env: variable is the
// process environment's, which does not change while it runs) from the provider's api_key_from, env:NAME or file:/path (modelcfg.ResolveCredential)
// and sent as a Bearer key; an empty api_key_from sends no Authorization
// header. The model's num_ctx and think options are Ollama-only
// and are ignored here, as is the request's Think; num_predict (or the
// request's MaxTokens, which wins) becomes max_tokens, and the overall timeout
// (or the request's Timeout, which wins) bounds the call. The first-token and idle timeouts do not apply,
// because the reply is not streamed.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

// defaultTimeout bounds a call whose model sets no overall timeout.
const defaultTimeout = 2 * time.Minute

// maxResponseBytes bounds what is read of a reply.
const maxResponseBytes = 8 << 20

// maxErrorBytes bounds how much of a provider's error text is kept.
const maxErrorBytes = 400

// Provider calls an OpenAI-compatible API.
type Provider struct {
	client   *http.Client
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

// WithTimeoutUnit changes the length of the "second" in the timeout option, so a
// test can exercise a timeout without waiting for it.
func WithTimeoutUnit(d time.Duration) Option { return func(p *Provider) { p.unit = d } }

// New returns an OpenAI-compatible provider.
func New(opts ...Option) *Provider {
	p := &Provider{client: &http.Client{}, getenv: os.Getenv, readFile: os.ReadFile, unit: time.Second}
	for _, o := range opts {
		o(p)
	}
	return p
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type jsonSchema struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type request struct {
	Model          string          `json:"model"`
	Messages       []message       `json:"messages"`
	Stream         bool            `json:"stream"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
}

type response struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// key looks the API key up. It is never logged or put in an error.
func (p *Provider) key(prov modelcfg.Provider) (string, error) {
	cred := modelcfg.ResolveCredential(prov, p.getenv, p.readFile)
	if !cred.Configured {
		return "", nil
	}
	return cred.Require(prov.ID)
}

// Complete sends the request's system prompt and input as a chat completion,
// asking for the reply schema when the step has one, and returns the text with
// the token counts the API reports (0 when it reports none). Every error is a
// *router.Failure.
func (p *Provider) Complete(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model, req router.Request) (router.Response, error) {
	if err := req.Validate(); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("openai: invalid request: %w", err)}
	}
	key, err := p.key(prov)
	if err != nil {
		// A configuration problem, not the model's: it must not trip the breaker.
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("openai: %w", err)}
	}
	headers := modelcfg.ResolveHeaders(prov, p.getenv, p.readFile)
	if err := headers.Require(prov.ID); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("openai: %w", err)}
	}

	system, turns := req.Conversation()
	var messages []message
	if system != "" {
		messages = append(messages, message{Role: "system", Content: system})
	}
	for _, t := range turns {
		messages = append(messages, message{Role: t.Role, Content: t.Content})
	}
	body := request{
		Model:     m.Name,
		Messages:  messages,
		MaxTokens: m.Options.NumPredict,
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = req.MaxTokens
	}
	if req.Schema != nil {
		body.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: jsonSchema{Name: "reply", Schema: req.Schema, Strict: true}}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("openai: encode the request: %w", err)}
	}

	timeout := time.Duration(m.Options.OverallTimeoutSeconds) * p.unit
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	endpoint := strings.TrimRight(prov.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("openai: build the request for provider %q: %w", prov.ID, err)}
	}
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	headers.Apply(httpReq)

	res, err := p.client.Do(httpReq)
	if err != nil {
		return router.Response{}, classifyTransport(ctx, callCtx, timeout, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return router.Response{}, classifyTransport(ctx, callCtx, timeout, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return router.Response{}, statusFailure(res.StatusCode, res.Header.Get("x-request-id"), raw)
	}

	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("openai: the reply is not JSON: %w", err)}
	}
	if len(out.Choices) == 0 {
		return router.Response{}, &router.Failure{Kind: router.FailServer, Err: errors.New("openai: the reply has no choices")}
	}
	return router.Response{
		Text:         out.Choices[0].Message.Content,
		InputTokens:  out.Usage.PromptTokens,
		OutputTokens: out.Usage.CompletionTokens,
	}, nil
}

// statusFailure classifies an HTTP error. The API's own message is kept, cut
// short; the request, and so the key, is never echoed.
func statusFailure(code int, requestID string, raw []byte) *router.Failure {
	msg := strings.TrimSpace(string(raw))
	var e apiError
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	msg = router.TruncateText(msg, maxErrorBytes)
	err := fmt.Errorf("openai: HTTP %d: %s", code, msg)
	switch {
	case code == http.StatusTooManyRequests:
		return &router.Failure{Kind: router.FailRateLimited, Err: err, HTTPStatus: code, RequestID: requestID}
	case code >= 500:
		return &router.Failure{Kind: router.FailServer, Err: err, HTTPStatus: code, RequestID: requestID}
	}
	return &router.Failure{Kind: router.FailClient, Err: err, HTTPStatus: code, RequestID: requestID}
}

// classifyTransport classifies a failure to talk to the API at all.
func classifyTransport(parent, call context.Context, timeout time.Duration, err error) *router.Failure {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case parent.Err() != nil:
		return &router.Failure{Kind: router.FailCanceled, Err: parent.Err()}
	case errors.Is(call.Err(), context.DeadlineExceeded):
		return &router.Failure{Kind: router.FailTimeout, Err: fmt.Errorf("openai: no answer within %s", timeout)}
	case errors.As(err, &dns):
		return &router.Failure{Kind: router.FailConnection, Err: fmt.Errorf("openai: %w", err)}
	case errors.As(err, &op) && op.Op == "dial":
		return &router.Failure{Kind: router.FailConnection, Err: fmt.Errorf("openai: %w", err)}
	}
	return &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("openai: %w", err)}
}

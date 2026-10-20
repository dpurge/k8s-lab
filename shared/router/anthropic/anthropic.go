// Package anthropic is the router's provider for Anthropic's Messages API,
// through POST {base_url}/v1/messages, called with plain net/http.
//
// The provider's headers (modelcfg.ResolveHeaders) are set after its own, so
// they may replace anthropic-version.
//
// The API key is read on every call (a file: is re-read; an env: variable is the
// process environment's, which does not change while it runs) from the
// provider's api_key_from, env:NAME or file:/path (modelcfg.ResolveCredential),
// sent as the x-api-key header, and never logged or put in an error. Without a
// credential the provider is unusable. base_url is
// optional and defaults to https://api.anthropic.com. The model's num_ctx and
// think options are Ollama-only and are ignored here, as is the request's
// Think; the request's MaxTokens, else num_predict, becomes max_tokens
// (default 16000), and the request's Timeout, else the overall timeout, bounds
// the call. The
// first-token and idle timeouts do not apply, because the reply is not streamed.
//
// The request carries no sampling parameters, tools or thinking settings,
// because the newest models reject them. When the step has a reply schema it is
// sent as output_config.format; the provider does not rewrite it, so it must
// satisfy Anthropic's limits (additionalProperties false on objects, no
// recursion, no numeric constraints). A schema Anthropic rejects is a 400 and
// surfaces as a client failure with Anthropic's message.
package anthropic

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

const (
	// DefaultBaseURL is used when the provider sets no base_url.
	DefaultBaseURL = "https://api.anthropic.com"

	// apiVersion is the anthropic-version header the API requires.
	apiVersion = "2023-06-01"

	// defaultMaxTokens applies when the model sets no num_predict; thinking
	// tokens on current models count against max_tokens, so it must be generous.
	defaultMaxTokens = 16000

	// defaultTimeout bounds a call whose model sets no overall timeout.
	defaultTimeout = 2 * time.Minute

	// maxResponseBytes bounds what is read of a reply.
	maxResponseBytes = 8 << 20

	// maxErrorBytes bounds how much of a provider's error text is kept.
	maxErrorBytes = 400

	// statusOverloaded is Anthropic's non-standard "overloaded" status.
	statusOverloaded = 529
)

// Provider calls the Anthropic Messages API.
type Provider struct {
	client   *http.Client
	getenv   func(string) string
	readFile func(string) ([]byte, error)
	baseURL  string
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

// WithBaseURL sets the address used when the provider has no base_url. It is a
// test knob, to check the default without reaching the real API.
func WithBaseURL(url string) Option { return func(p *Provider) { p.baseURL = url } }

// New returns an Anthropic provider.
func New(opts ...Option) *Provider {
	p := &Provider{client: &http.Client{}, getenv: os.Getenv, readFile: os.ReadFile, baseURL: DefaultBaseURL, unit: time.Second}
	for _, o := range opts {
		o(p)
	}
	return p
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type outputFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema"`
}

type outputConfig struct {
	Format outputFormat `json:"format"`
}

type request struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	System       string        `json:"system,omitempty"`
	Messages     []message     `json:"messages"`
	OutputConfig *outputConfig `json:"output_config,omitempty"`
}

type response struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason  string `json:"stop_reason"`
	StopDetails struct {
		Category string `json:"category"`
	} `json:"stop_details"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	RequestID string `json:"request_id"`
}

type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// key looks the API key up. It is never logged or put in an error.
func (p *Provider) key(prov modelcfg.Provider) (string, error) {
	return modelcfg.ResolveCredential(prov, p.getenv, p.readFile).Require(prov.ID)
}

// Complete sends the request's system prompt and input as a message, asking for
// the reply schema when the step has one, and returns the concatenated text
// blocks with the token counts the API reports (0 when it reports none). Every
// error is a *router.Failure.
func (p *Provider) Complete(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model, req router.Request) (router.Response, error) {
	if err := req.Validate(); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("anthropic: invalid request: %w", err)}
	}
	key, err := p.key(prov)
	if err != nil {
		// A configuration problem, not the model's: it must not trip the breaker.
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("anthropic: %w", err)}
	}
	headers := modelcfg.ResolveHeaders(prov, p.getenv, p.readFile)
	if err := headers.Require(prov.ID); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("anthropic: %w", err)}
	}

	maxTokens := m.Options.NumPredict
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	system, turns := req.Conversation()
	messages := make([]message, len(turns))
	for i, t := range turns {
		messages[i] = message{Role: t.Role, Content: t.Content}
	}
	body := request{
		Model:     m.Name,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  messages,
	}
	if req.Schema != nil {
		body.OutputConfig = &outputConfig{Format: outputFormat{Type: "json_schema", Schema: req.Schema}}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("anthropic: encode the request: %w", err)}
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
	base := prov.BaseURL
	if base == "" {
		base = p.baseURL
	}
	endpoint := strings.TrimRight(base, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("anthropic: build the request for provider %q: %w", prov.ID, err)}
	}
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("Content-Type", "application/json")
	headers.Apply(httpReq)

	res, err := p.client.Do(httpReq)
	if err != nil {
		return router.Response{}, classifyTransport(ctx, callCtx, timeout, err)
	}
	defer res.Body.Close()
	headerID := res.Header.Get("request-id")
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return router.Response{}, classifyTransport(ctx, callCtx, timeout, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return router.Response{}, statusFailure(res.StatusCode, headerID, raw)
	}

	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return router.Response{}, &router.Failure{
			Kind: router.FailServer, Err: fmt.Errorf("anthropic: the reply is not JSON: %w", err),
			HTTPStatus: res.StatusCode, RequestID: headerID,
		}
	}
	requestID := firstNonEmpty(headerID, out.RequestID)
	if out.StopReason == "refusal" {
		// The model declined this request; it is healthy, so the breaker must
		// not trip, but the next model should be tried.
		msg := "anthropic: the model refused to answer"
		if out.StopDetails.Category != "" {
			msg += " (category " + out.StopDetails.Category + ")"
		}
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: errors.New(msg), HTTPStatus: res.StatusCode, RequestID: requestID}
	}

	var text strings.Builder
	hasText := false
	for _, block := range out.Content {
		if block.Type == "text" {
			hasText = true
			text.WriteString(block.Text)
		}
	}
	if !hasText {
		return router.Response{}, &router.Failure{
			Kind: router.FailServer, Err: fmt.Errorf("anthropic: the reply has no text block (stop_reason %q)", out.StopReason),
			HTTPStatus: res.StatusCode, RequestID: requestID,
		}
	}
	return router.Response{
		Text:         text.String(),
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}, nil
}

// statusFailure classifies an HTTP error. The API's own type and message are
// kept, cut short; the request, and so the key and the prompt, is never echoed.
func statusFailure(code int, headerID string, raw []byte) *router.Failure {
	var e apiError
	requestID := headerID
	var err error
	if json.Unmarshal(raw, &e) == nil && (e.Error.Type != "" || e.Error.Message != "") {
		requestID = firstNonEmpty(headerID, e.RequestID)
		err = fmt.Errorf("anthropic: %s: %s", e.Error.Type, router.TruncateText(e.Error.Message, maxErrorBytes))
	} else {
		err = fmt.Errorf("anthropic: the error reply is not an API error: %s", router.TruncateText(strings.TrimSpace(string(raw)), maxErrorBytes))
	}
	kind := router.FailClient
	switch {
	case code == http.StatusTooManyRequests || code == statusOverloaded:
		kind = router.FailRateLimited
	case code >= 500:
		kind = router.FailServer
	}
	return &router.Failure{Kind: kind, Err: err, HTTPStatus: code, RequestID: requestID}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// classifyTransport classifies a failure to talk to the API at all.
func classifyTransport(parent, call context.Context, timeout time.Duration, err error) *router.Failure {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case parent.Err() != nil:
		return &router.Failure{Kind: router.FailCanceled, Err: parent.Err()}
	case errors.Is(call.Err(), context.DeadlineExceeded):
		return &router.Failure{Kind: router.FailTimeout, Err: fmt.Errorf("anthropic: no answer within %s", timeout)}
	case errors.As(err, &dns):
		return &router.Failure{Kind: router.FailConnection, Err: fmt.Errorf("anthropic: %w", err)}
	case errors.As(err, &op) && op.Op == "dial":
		return &router.Failure{Kind: router.FailConnection, Err: fmt.Errorf("anthropic: %w", err)}
	}
	return &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("anthropic: %w", err)}
}

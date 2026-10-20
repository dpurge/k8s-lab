// Package vertex is the router's provider for Google's Vertex AI (Gemini models),
// through the generateContent REST API. It needs an OAuth access token, which it
// reads on every call (modelcfg.ResolveCredential) from the provider's
// api_key_from (file:/path or env:NAME), so a refreshed token file (a mounted Secret that
// changes) is used at once without a restart; an env: token is the process
// environment's, which does not change while it runs. Without a token the provider is
// unusable.
//
// The provider's headers (modelcfg.ResolveHeaders) are set after its own.
//
// The model's num_predict becomes maxOutputTokens and its overall timeout bounds
// the call; the request's MaxTokens and Timeout, when set, win over both. The
// request's Think is ignored.
package vertex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

// Provider calls Vertex AI.
type Provider struct {
	getenv   func(string) string
	readFile func(string) ([]byte, error)
	client   *http.Client
	endpoint func(location string) string
	// unit is what one "second" of a timeout option lasts; tests shorten it.
	unit time.Duration
}

var _ router.Provider = (*Provider)(nil)

// Option configures a Provider.
type Option func(*Provider)

// WithEndpoint replaces how the API's address is built from a location; tests
// point it at a local server.
func WithEndpoint(f func(location string) string) Option { return func(p *Provider) { p.endpoint = f } }

// WithTimeoutUnit changes the length of the "second" in the timeout option, so a
// test can exercise a timeout without waiting for it.
func WithTimeoutUnit(d time.Duration) Option { return func(p *Provider) { p.unit = d } }

// WithGetenv replaces how the token is looked up by variable name; tests use it
// instead of the process environment.
func WithGetenv(f func(string) string) Option { return func(p *Provider) { p.getenv = f } }

// WithReadFile replaces how an api_key_from file: is read; tests use it instead of the
// file system.
func WithReadFile(f func(string) ([]byte, error)) Option { return func(p *Provider) { p.readFile = f } }

// New returns a Vertex provider; the access token comes from the provider
// configuration (api_key_from) on every call.
func New(opts ...Option) *Provider {
	p := &Provider{getenv: os.Getenv, readFile: os.ReadFile, client: &http.Client{}, endpoint: defaultEndpoint, unit: time.Second}
	for _, o := range opts {
		o(p)
	}
	return p
}

func defaultEndpoint(location string) string {
	if location == "global" {
		return "https://aiplatform.googleapis.com"
	}
	return "https://" + location + "-aiplatform.googleapis.com"
}

type part struct {
	Text string `json:"text"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type generationConfig struct {
	Temperature      float64 `json:"temperature"`
	MaxOutputTokens  int     `json:"maxOutputTokens,omitempty"`
	ResponseMimeType string  `json:"responseMimeType,omitempty"`
}

type request struct {
	SystemInstruction *content         `json:"systemInstruction,omitempty"`
	Contents          []content        `json:"contents"`
	GenerationConfig  generationConfig `json:"generationConfig"`
}

type response struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokens   int `json:"promptTokenCount"`
		CandidateToken int `json:"candidatesTokenCount"`
		ThoughtsTokens int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// token reads the access token. It is never logged or put in an error.
func (p *Provider) token(prov modelcfg.Provider) (string, error) {
	token, err := modelcfg.ResolveCredential(prov, p.getenv, p.readFile).Require(prov.ID)
	if err != nil {
		return "", fmt.Errorf("%w (dev: task refresh-assistant-vertex-token writes the access token file)", err)
	}
	return token, nil
}

// Complete sends the request's system prompt and input as a Gemini
// generateContent call, asking for JSON when the step has a reply schema (the
// engine validates the reply itself), and returns the text with the token
// counts the API reports. Every error is an *router.Failure.
func (p *Provider) Complete(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model, req router.Request) (router.Response, error) {
	if err := req.Validate(); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("vertex: invalid request: %w", err)}
	}
	token, err := p.token(prov)
	if err != nil {
		// A configuration problem, not the model's: it must not trip the breaker.
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("vertex: %w", err)}
	}
	headers := modelcfg.ResolveHeaders(prov, p.getenv, p.readFile)
	if err := headers.Require(prov.ID); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("vertex: %w", err)}
	}

	maxOutputTokens := m.Options.NumPredict
	if req.MaxTokens > 0 {
		maxOutputTokens = req.MaxTokens
	}
	system, turns := req.Conversation()
	contents := make([]content, len(turns))
	for i, t := range turns {
		role := "user"
		if t.Role == router.RoleAssistant {
			role = "model"
		}
		contents[i] = content{Role: role, Parts: []part{{Text: t.Content}}}
	}
	body := request{
		Contents:         contents,
		GenerationConfig: generationConfig{MaxOutputTokens: maxOutputTokens},
	}
	if system != "" {
		body.SystemInstruction = &content{Parts: []part{{Text: system}}}
	}
	if len(req.Schema) > 0 {
		body.GenerationConfig.ResponseMimeType = "application/json"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("vertex: encode the request: %w", err)}
	}

	endpoint := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		p.endpoint(prov.Location), url.PathEscape(prov.Project), url.PathEscape(prov.Location), url.PathEscape(m.Name))
	timeout := time.Duration(m.Options.OverallTimeoutSeconds) * p.unit
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("vertex: build the request: %w", err)}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
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
	if res.StatusCode != http.StatusOK {
		return router.Response{}, statusFailure(res.StatusCode, raw)
	}

	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return router.Response{}, &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("vertex: the reply is not JSON: %w", err)}
	}
	if len(out.Candidates) == 0 {
		reason := out.PromptFeedback.BlockReason
		if reason == "" {
			reason = "no candidates"
		}
		return router.Response{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("vertex: the model gave no answer (%s)", reason)}
	}
	var text strings.Builder
	for _, pt := range out.Candidates[0].Content.Parts {
		text.WriteString(pt.Text)
	}
	return router.Response{
		Text:         text.String(),
		InputTokens:  out.UsageMetadata.PromptTokens,
		OutputTokens: out.UsageMetadata.CandidateToken + out.UsageMetadata.ThoughtsTokens,
	}, nil
}

// statusFailure classifies an HTTP error. The API's own message is kept, cut
// short; it never holds the token or the prompt.
func statusFailure(code int, raw []byte) *router.Failure {
	msg := strings.TrimSpace(string(raw))
	var e apiError
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
		if e.Error.Status != "" {
			msg = e.Error.Status + ": " + msg
		}
	}
	msg = router.TruncateText(msg, maxErrorBytes)
	err := fmt.Errorf("vertex: HTTP %d: %s", code, msg)
	switch {
	case code == http.StatusTooManyRequests:
		return &router.Failure{Kind: router.FailRateLimited, Err: err, HTTPStatus: code}
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("%w (the access token may have expired: task refresh-assistant-vertex-token)", err), HTTPStatus: code}
	case code >= 500:
		return &router.Failure{Kind: router.FailServer, Err: err, HTTPStatus: code}
	}
	return &router.Failure{Kind: router.FailClient, Err: err, HTTPStatus: code}
}

// classifyTransport classifies a failure to talk to the API at all.
func classifyTransport(parent, call context.Context, timeout time.Duration, err error) *router.Failure {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case parent.Err() != nil:
		return &router.Failure{Kind: router.FailCanceled, Err: parent.Err()}
	case errors.Is(call.Err(), context.DeadlineExceeded):
		return &router.Failure{Kind: router.FailTimeout, Err: fmt.Errorf("vertex: no answer within %s", timeout)}
	case errors.As(err, &dns):
		return &router.Failure{Kind: router.FailConnection, Err: fmt.Errorf("vertex: %w", err)}
	case errors.As(err, &op) && op.Op == "dial":
		return &router.Failure{Kind: router.FailConnection, Err: fmt.Errorf("vertex: %w", err)}
	}
	return &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("vertex: %w", err)}
}

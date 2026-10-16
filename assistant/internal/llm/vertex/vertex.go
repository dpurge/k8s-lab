// Package vertex is the router's provider for Google's Vertex AI (Gemini models),
// through the generateContent REST API. It needs an OAuth access token, which it
// reads from a file on every call, so a refreshed token (a mounted Secret that
// changes) is used at once without a restart.
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

	"assistant/internal/llm"
	"assistant/internal/models"
	"assistant/internal/workflow"
)

// defaultTimeout bounds a call whose model sets no overall timeout.
const defaultTimeout = 2 * time.Minute

// maxResponseBytes bounds what is read of a reply.
const maxResponseBytes = 8 << 20

// maxErrorBytes bounds how much of a provider's error text is kept.
const maxErrorBytes = 400

// Provider calls Vertex AI.
type Provider struct {
	tokenFile string
	client    *http.Client
	endpoint  func(location string) string
	// unit is what one "second" of a timeout option lasts; tests shorten it.
	unit time.Duration
}

var _ llm.Provider = (*Provider)(nil)

// Option configures a Provider.
type Option func(*Provider)

// WithEndpoint replaces how the API's address is built from a location; tests
// point it at a local server.
func WithEndpoint(f func(location string) string) Option { return func(p *Provider) { p.endpoint = f } }

// WithTimeoutUnit changes the length of the "second" in the timeout option, so a
// test can exercise a timeout without waiting for it.
func WithTimeoutUnit(d time.Duration) Option { return func(p *Provider) { p.unit = d } }

// New returns a Vertex provider that reads its access token from tokenFile.
func New(tokenFile string, opts ...Option) *Provider {
	p := &Provider{tokenFile: tokenFile, client: &http.Client{}, endpoint: defaultEndpoint, unit: time.Second}
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
func (p *Provider) token() (string, error) {
	if p.tokenFile == "" {
		return "", errors.New("no access token file is configured")
	}
	raw, err := os.ReadFile(p.tokenFile)
	if err != nil {
		return "", fmt.Errorf("cannot read the access token file %s: %w (create it with task refresh-assistant-vertex-token)", p.tokenFile, errors.Unwrap(err))
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("the access token file %s is empty (refresh it with task refresh-assistant-vertex-token)", p.tokenFile)
	}
	return token, nil
}

// Complete sends the request's system prompt and input as a Gemini
// generateContent call, asking for JSON when the step has a reply schema (the
// engine validates the reply itself), and returns the text with the token
// counts the API reports. Every error is an *llm.Failure.
func (p *Provider) Complete(ctx context.Context, prov models.Provider, m models.Model, req workflow.Request) (workflow.Response, error) {
	token, err := p.token()
	if err != nil {
		// A configuration problem, not the model's: it must not trip the breaker.
		return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: fmt.Errorf("vertex: %w", err)}
	}

	body := request{
		Contents:         []content{{Role: "user", Parts: []part{{Text: req.Input}}}},
		GenerationConfig: generationConfig{MaxOutputTokens: m.Options.NumPredict},
	}
	if req.System != "" {
		body.SystemInstruction = &content{Parts: []part{{Text: req.System}}}
	}
	if len(req.Schema) > 0 {
		body.GenerationConfig.ResponseMimeType = "application/json"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: fmt.Errorf("vertex: encode the request: %w", err)}
	}

	endpoint := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		p.endpoint(prov.Location), url.PathEscape(prov.Project), url.PathEscape(prov.Location), url.PathEscape(m.Name))
	timeout := time.Duration(m.Options.OverallTimeoutSeconds) * p.unit
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: fmt.Errorf("vertex: build the request: %w", err)}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := p.client.Do(httpReq)
	if err != nil {
		return workflow.Response{}, classifyTransport(ctx, callCtx, timeout, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return workflow.Response{}, classifyTransport(ctx, callCtx, timeout, err)
	}
	if res.StatusCode != http.StatusOK {
		return workflow.Response{}, statusFailure(res.StatusCode, raw)
	}

	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return workflow.Response{}, &llm.Failure{Kind: llm.FailServer, Err: fmt.Errorf("vertex: the reply is not JSON: %w", err)}
	}
	if len(out.Candidates) == 0 {
		reason := out.PromptFeedback.BlockReason
		if reason == "" {
			reason = "no candidates"
		}
		return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: fmt.Errorf("vertex: the model gave no answer (%s)", reason)}
	}
	var text strings.Builder
	for _, pt := range out.Candidates[0].Content.Parts {
		text.WriteString(pt.Text)
	}
	return workflow.Response{
		Text:         text.String(),
		InputTokens:  out.UsageMetadata.PromptTokens,
		OutputTokens: out.UsageMetadata.CandidateToken + out.UsageMetadata.ThoughtsTokens,
	}, nil
}

// statusFailure classifies an HTTP error. The API's own message is kept, cut
// short; it never holds the token or the prompt.
func statusFailure(code int, raw []byte) *llm.Failure {
	msg := strings.TrimSpace(string(raw))
	var e apiError
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
		if e.Error.Status != "" {
			msg = e.Error.Status + ": " + msg
		}
	}
	if len(msg) > maxErrorBytes {
		msg = msg[:maxErrorBytes] + "…"
	}
	err := fmt.Errorf("vertex: HTTP %d: %s", code, msg)
	switch {
	case code == http.StatusTooManyRequests:
		return &llm.Failure{Kind: llm.FailRateLimited, Err: err}
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return &llm.Failure{Kind: llm.FailClient, Err: fmt.Errorf("%w (the access token may have expired: task refresh-assistant-vertex-token)", err)}
	case code >= 500:
		return &llm.Failure{Kind: llm.FailServer, Err: err}
	}
	return &llm.Failure{Kind: llm.FailClient, Err: err}
}

// classifyTransport classifies a failure to talk to the API at all.
func classifyTransport(parent, call context.Context, timeout time.Duration, err error) *llm.Failure {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case parent.Err() != nil:
		return &llm.Failure{Kind: llm.FailCanceled, Err: parent.Err()}
	case errors.Is(call.Err(), context.DeadlineExceeded):
		return &llm.Failure{Kind: llm.FailTimeout, Err: fmt.Errorf("vertex: no answer within %s", timeout)}
	case errors.As(err, &dns):
		return &llm.Failure{Kind: llm.FailConnection, Err: fmt.Errorf("vertex: %w", err)}
	case errors.As(err, &op) && op.Op == "dial":
		return &llm.Failure{Kind: llm.FailConnection, Err: fmt.Errorf("vertex: %w", err)}
	}
	return &llm.Failure{Kind: llm.FailServer, Err: fmt.Errorf("vertex: %w", err)}
}

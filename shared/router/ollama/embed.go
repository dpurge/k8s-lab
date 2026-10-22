package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

// defaultEmbedTimeout bounds an embedding call whose model sets no overall
// timeout.
const defaultEmbedTimeout = 2 * time.Minute

// maxEmbedResponseBytes bounds what is read of an embedding reply.
const maxEmbedResponseBytes = 8 << 20

// maxEmbedErrorBytes bounds how much of an error text is kept.
const maxEmbedErrorBytes = 400

var _ router.EmbeddingProvider = (*Provider)(nil)

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings      [][]float32 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

// Embed makes one embedding through POST {base_url}/api/embed. The model's
// overall timeout (or the request's Timeout, which wins) bounds the call; the
// credential and headers are the ones of Complete. The reply must hold exactly
// one vector; its length against the model's dimension is checked by the router.
func (p *Provider) Embed(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model, req router.EmbedRequest) (router.EmbedResponse, error) {
	if strings.TrimSpace(req.Input) == "" {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: errors.New("ollama: invalid embedding request: the input is empty")}
	}
	key, err := p.key(prov)
	if err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("ollama: %w", err)}
	}
	headers := modelcfg.ResolveHeaders(prov, p.getenv, p.readFile)
	if err := headers.Require(prov.ID); err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("ollama: %w", err)}
	}
	payload, err := json.Marshal(embedRequest{Model: m.Name, Input: req.Input})
	if err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("ollama: encode the embedding request: %w", err)}
	}

	timeout := time.Duration(m.Options.OverallTimeoutSeconds) * p.unit
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	if timeout <= 0 {
		timeout = defaultEmbedTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	endpoint := strings.TrimRight(prov.BaseURL, "/") + "/api/embed"
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("ollama: build the embedding request for provider %q: %w", prov.ID, err)}
	}
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	headers.Apply(httpReq)

	res, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return router.EmbedResponse{}, embedTransportFailure(ctx, callCtx, timeout, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxEmbedResponseBytes))
	if err != nil {
		return router.EmbedResponse{}, embedTransportFailure(ctx, callCtx, timeout, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return router.EmbedResponse{}, embedStatusFailure(res.StatusCode, raw)
	}
	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("ollama: the embedding reply is not JSON: %w", err)}
	}
	if len(out.Embeddings) != 1 {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("ollama: the embedding reply has %d vectors, want 1", len(out.Embeddings))}
	}
	return router.EmbedResponse{Vector: out.Embeddings[0], InputTokens: out.PromptEvalCount}, nil
}

// embedStatusFailure classifies an HTTP error. Ollama's own message ({"error":
// "..."}) is kept, cut to a bounded size.
func embedStatusFailure(code int, raw []byte) *router.Failure {
	msg := strings.TrimSpace(string(raw))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	err := fmt.Errorf("ollama: HTTP %d: %s", code, router.TruncateText(msg, maxEmbedErrorBytes))
	switch {
	case code == http.StatusTooManyRequests:
		return &router.Failure{Kind: router.FailRateLimited, Err: err, HTTPStatus: code}
	case code >= 500:
		return &router.Failure{Kind: router.FailServer, Err: err, HTTPStatus: code}
	}
	return &router.Failure{Kind: router.FailClient, Err: err, HTTPStatus: code}
}

// embedTransportFailure classifies a failure to talk to the server at all.
func embedTransportFailure(parent, call context.Context, timeout time.Duration, err error) *router.Failure {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case parent.Err() != nil:
		return &router.Failure{Kind: router.FailCanceled, Err: parent.Err()}
	case errors.Is(call.Err(), context.DeadlineExceeded):
		return &router.Failure{Kind: router.FailTimeout, Err: fmt.Errorf("ollama: no embedding within %s", timeout)}
	case errors.As(err, &dns):
		return &router.Failure{Kind: router.FailConnection, Err: fmt.Errorf("ollama: %w", err)}
	case errors.As(err, &op) && op.Op == "dial":
		return &router.Failure{Kind: router.FailConnection, Err: fmt.Errorf("ollama: %w", err)}
	}
	return &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("ollama: %w", err)}
}

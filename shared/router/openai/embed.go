package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

var _ router.EmbeddingProvider = (*Provider)(nil)

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
}

// Embed makes one embedding through POST {base_url}/embeddings. The credential,
// headers, overall timeout and error classification are the ones of Complete.
// The reply must hold exactly one vector; its length against the model's
// dimension is checked by the router. The request carries only the model and
// the input: a server that supports a "dimensions" parameter is not asked to
// shorten the vector, so a mismatch shows up as a failed call.
func (p *Provider) Embed(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model, req router.EmbedRequest) (router.EmbedResponse, error) {
	if strings.TrimSpace(req.Input) == "" {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: errors.New("openai: invalid embedding request: the input is empty")}
	}
	key, err := p.key(prov)
	if err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("openai: %w", err)}
	}
	headers := modelcfg.ResolveHeaders(prov, p.getenv, p.readFile)
	if err := headers.Require(prov.ID); err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("openai: %w", err)}
	}
	payload, err := json.Marshal(embedRequest{Model: m.Name, Input: req.Input})
	if err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("openai: encode the embedding request: %w", err)}
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
	endpoint := strings.TrimRight(prov.BaseURL, "/") + "/embeddings"
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailClient, Err: fmt.Errorf("openai: build the embedding request for provider %q: %w", prov.ID, err)}
	}
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	headers.Apply(httpReq)

	res, err := p.client.Do(httpReq)
	if err != nil {
		return router.EmbedResponse{}, classifyTransport(ctx, callCtx, timeout, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return router.EmbedResponse{}, classifyTransport(ctx, callCtx, timeout, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return router.EmbedResponse{}, statusFailure(res.StatusCode, res.Header.Get("x-request-id"), raw)
	}
	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("openai: the embedding reply is not JSON: %w", err)}
	}
	if len(out.Data) != 1 {
		return router.EmbedResponse{}, &router.Failure{Kind: router.FailServer, Err: fmt.Errorf("openai: the embedding reply has %d vectors, want 1", len(out.Data))}
	}
	return router.EmbedResponse{Vector: out.Data[0].Embedding, InputTokens: out.Usage.PromptTokens}, nil
}

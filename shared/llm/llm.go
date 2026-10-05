package llm

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
	"sync/atomic"
	"time"
)

type Config struct {
	Provider, BaseURL, APIKey, Model string
	// NumCtx sets Ollama's runtime context window (options.num_ctx). Zero
	// means omit it, leaving Ollama's own default in effect. No effect on
	// the openAI path, which has no equivalent per-request knob.
	NumCtx int
	// Think enables Ollama's hidden reasoning trace on thinking-capable
	// models. Zero value (false) preserves prior behavior for every
	// existing caller. No effect on the openAI path, which has no
	// equivalent per-request knob.
	Think bool
	// Timeout caps the whole call, however steadily the model is producing
	// output — a backstop against runaway generation, not the primary hang
	// detector (FirstTokenTimeout/IdleTimeout are). Zero means
	// defaultTimeout.
	Timeout time.Duration
	// FirstTokenTimeout bounds the wait for the first streamed chunk on the
	// Ollama path, which covers model load plus prompt evaluation. Zero
	// means defaultFirstTokenTimeout.
	FirstTokenTimeout time.Duration
	// IdleTimeout bounds the gap between streamed chunks once output has
	// started on the Ollama path, so a stalled model fails in seconds while
	// a slow-but-progressing one (≈4 tokens/s on a CPU-only node) keeps
	// going. Zero means defaultIdleTimeout.
	IdleTimeout time.Duration
	// Format is sent as Ollama's "format" field — a JSON schema that
	// constrains the reply to schema-shaped JSON (structured outputs;
	// verified with stream: true on Ollama 0.34.1). Empty omits it. No
	// effect on the openAI path; callers must still validate the reply.
	Format json.RawMessage
}
type Message struct {
	Role, Content, ToolName string
	ToolCalls               []ToolCall
}
type ToolCall struct {
	Function struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	} `json:"function"`
}
type Response struct {
	Content   string
	ToolCalls []ToolCall

	// PromptTokens and ReplyTokens are the token counts Ollama reports for
	// the call (prompt_eval_count, eval_count), zero when the provider did
	// not report them. Their sum reaching the context window means Ollama
	// silently cut the prompt or shifted the context during the reply.
	PromptTokens int
	ReplyTokens  int
}
type Client struct {
	cfg Config
	// http serves the non-streaming openAI path, bounded by the overall
	// timeout alone.
	http *http.Client
	// stream serves the streaming Ollama path; it has no client timeout of
	// its own because all three limits are enforced per call (see
	// ollamaStream).
	stream                         *http.Client
	firstTokenTimeout, idleTimeout time.Duration
}

// Defaults applied when the matching Config field is zero. The overall cap
// is generous because the streaming limits below are what catch a hang;
// measured on a CPU-only prod node, a 942-character translation took 300s
// while producing a token every ~0.25s.
const (
	defaultTimeout           = 30 * time.Minute
	defaultFirstTokenTimeout = 5 * time.Minute
	defaultIdleTimeout       = 60 * time.Second
)

// Limit names which bound ended a call, for TimeoutError and callers' logs.
const (
	LimitFirstToken = "first_token"
	LimitIdle       = "idle"
	LimitOverall    = "overall"
)

// TimeoutError reports that a call was aborted by one of its limits.
type TimeoutError struct {
	Limit string
	Model string
	After time.Duration
}

func (e *TimeoutError) Error() string {
	switch e.Limit {
	case LimitFirstToken:
		return fmt.Sprintf("no output from model %s within %gs (model load + prompt processing)", e.Model, e.After.Seconds())
	case LimitIdle:
		return fmt.Sprintf("model %s stalled: no output for %gs", e.Model, e.After.Seconds())
	default:
		return fmt.Sprintf("model %s exceeded overall timeout of %gs", e.Model, e.After.Seconds())
	}
}

// StatusError is a non-2xx reply from the provider. Its text is the one callers
// have always seen; the type lets IsRetryable tell a 5xx from a 4xx.
type StatusError struct {
	StatusCode int
	Status     string
	Body       []byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("chat provider returned %s: %s", e.Status, e.Body)
}

// ErrStreamIncomplete matches (errors.Is) a streamed reply that ended early or
// carried an error chunk, without changing those errors' text.
var ErrStreamIncomplete = errors.New("chat provider stream incomplete")

type streamError struct{ msg string }

func (e streamError) Error() string        { return e.msg }
func (e streamError) Is(target error) bool { return target == ErrStreamIncomplete }

// IsRetryable reports whether err is a transient failure worth another
// attempt: a 5xx, a stream that ended early or errored, an idle stall, or a
// connection-level failure. First-token and overall timeouts are not retried
// (another attempt would only cost another long wait), nor are 4xx replies or
// the caller's own cancellation.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var te *TimeoutError
	if errors.As(err, &te) {
		return te.Limit == LimitIdle
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.StatusCode >= 500
	}
	if errors.Is(err, ErrStreamIncomplete) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && !ne.Timeout()
}

// New creates an LLM client, applying the default for each zero timeout.
func New(c Config) *Client {
	return &Client{
		cfg:               c,
		http:              &http.Client{Timeout: orDefault(c.Timeout, defaultTimeout)},
		stream:            &http.Client{},
		firstTokenTimeout: orDefault(c.FirstTokenTimeout, defaultFirstTokenTimeout),
		idleTimeout:       orDefault(c.IdleTimeout, defaultIdleTimeout),
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d
}

func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	r, err := c.Chat(ctx, messages, nil)
	return r.Content, err
}

func (c *Client) Chat(ctx context.Context, messages []Message, tools []map[string]any) (Response, error) {
	// "openai" and "openrouter" are both OpenAI-compatible APIs, so route
	// either provider name to the openAI path directly now that provider
	// names are a first-class registry concept (phraseforge's
	// providers.ollama/providers.openrouter). The BaseURL substring check
	// stays alongside it for backward compatibility with any caller that
	// doesn't set Provider to one of these exact strings.
	if c.cfg.Provider == "openai" || c.cfg.Provider == "openrouter" || strings.Contains(c.cfg.BaseURL, "openrouter.ai") {
		return c.openAI(ctx, messages, tools)
	}
	return c.ollama(ctx, messages, tools)
}

func (c *Client) ollama(ctx context.Context, messages []Message, tools []map[string]any) (Response, error) {
	ms := make([]map[string]any, len(messages))
	for i, m := range messages {
		mm := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			mm["tool_calls"] = m.ToolCalls
		}
		if m.ToolName != "" {
			mm["tool_name"] = m.ToolName
		}
		ms[i] = mm
	}
	// think is configurable per purpose/prompt (Config.Think) but defaults
	// to false: a hidden reasoning trace on a thinking-capable model (e.g.
	// gemma4) turned a ~1s answer into 148s — confirmed by direct testing.
	// Callers should only opt in where that trade-off is wanted; it's a
	// no-op for models without a thinking capability.
	in := map[string]any{"model": c.cfg.Model, "messages": ms, "stream": true, "think": c.cfg.Think}
	if len(tools) > 0 {
		in["tools"] = tools
	}
	if c.cfg.NumCtx > 0 {
		in["options"] = map[string]any{"num_ctx": c.cfg.NumCtx}
	}
	if len(c.cfg.Format) > 0 {
		in["format"] = c.cfg.Format
	}
	return c.ollamaStream(ctx, strings.TrimRight(c.cfg.BaseURL, "/")+"/api/chat", in)
}

// ollamaStreamChunk is one NDJSON line of a streaming /api/chat response.
type ollamaStreamChunk struct {
	Message struct {
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
	Done         bool   `json:"done"`
	Error        string `json:"error"`
	PromptTokens int    `json:"prompt_eval_count"`
	ReplyTokens  int    `json:"eval_count"`
}

// ollamaStream reads a streaming /api/chat response under three limits: a
// wait for the first chunk carrying output (load + prompt eval), a maximum
// gap between such chunks afterwards, and an overall cap. Whichever fires
// cancels the request and is returned as a *TimeoutError. Thinking-only
// chunks count as output, since a reasoning model is making progress.
func (c *Client) ollamaStream(ctx context.Context, url string, in map[string]any) (Response, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	overall := orDefault(c.cfg.Timeout, defaultTimeout)
	overallTimer := time.AfterFunc(overall, func() {
		cancel(&TimeoutError{Limit: LimitOverall, Model: c.cfg.Model, After: overall})
	})
	defer overallTimer.Stop()
	var started atomic.Bool
	progressTimer := time.AfterFunc(c.firstTokenTimeout, func() {
		if started.Load() {
			cancel(&TimeoutError{Limit: LimitIdle, Model: c.cfg.Model, After: c.idleTimeout})
			return
		}
		cancel(&TimeoutError{Limit: LimitFirstToken, Model: c.cfg.Model, After: c.firstTokenTimeout})
	})
	defer progressTimer.Stop()

	resp, err := c.do(ctx, c.stream, url, in)
	if err != nil {
		return Response{}, limitOr(ctx, err)
	}
	defer resp.Body.Close()

	var content strings.Builder
	var toolCalls []ToolCall
	dec := json.NewDecoder(resp.Body)
	for {
		var chunk ollamaStreamChunk
		if err := dec.Decode(&chunk); err != nil {
			if errors.Is(err, io.EOF) {
				return Response{}, streamError{"chat provider stream ended before completion"}
			}
			return Response{}, limitOr(ctx, err)
		}
		if chunk.Error != "" {
			return Response{}, streamError{"chat provider stream error: " + chunk.Error}
		}
		if chunk.Message.Content != "" || chunk.Message.Thinking != "" || len(chunk.Message.ToolCalls) > 0 {
			started.Store(true)
			progressTimer.Reset(c.idleTimeout)
		}
		content.WriteString(chunk.Message.Content)
		toolCalls = append(toolCalls, chunk.Message.ToolCalls...)
		if chunk.Done {
			return Response{Content: content.String(), ToolCalls: toolCalls, PromptTokens: chunk.PromptTokens, ReplyTokens: chunk.ReplyTokens}, nil
		}
	}
}

// limitOr returns the *TimeoutError that cancelled ctx, if one did, instead
// of the transport error it caused; otherwise err unchanged (including a
// caller's own cancellation).
func limitOr(ctx context.Context, err error) error {
	var te *TimeoutError
	if errors.As(context.Cause(ctx), &te) {
		return te
	}
	return err
}

func (c *Client) openAI(ctx context.Context, messages []Message, tools []map[string]any) (Response, error) {
	ms := make([]map[string]any, len(messages))
	for i, m := range messages {
		mm := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			mm["tool_calls"] = m.ToolCalls
		}
		ms[i] = mm
	}
	in := map[string]any{"model": c.cfg.Model, "messages": ms}
	if len(tools) > 0 {
		in["tools"] = tools
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.post(ctx, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", in, &out); err != nil {
		return Response{}, err
	}
	if len(out.Choices) == 0 {
		return Response{}, fmt.Errorf("chat provider returned no choices")
	}
	return Response{Content: out.Choices[0].Message.Content, ToolCalls: out.Choices[0].Message.ToolCalls}, nil
}

func (c *Client) post(ctx context.Context, url string, in, out any) error {
	resp, err := c.do(ctx, c.http, url, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// do sends in as a JSON POST and returns the response once its status is
// known to be 2xx; a non-2xx status becomes an error carrying the body.
func (c *Client) do(ctx context.Context, hc *http.Client, url string, in any) (*http.Response, error) {
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		bb, _ := io.ReadAll(resp.Body)
		return nil, &StatusError{StatusCode: resp.StatusCode, Status: resp.Status, Body: bb}
	}
	return resp, nil
}

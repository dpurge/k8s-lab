package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
}
type Client struct {
	cfg  Config
	http *http.Client
}

// New creates an LLM client. The timeout covers provider queueing and
// response-body transfer while enforcing a two-minute maximum.
func New(c Config) *Client {
	return &Client{cfg: c, http: &http.Client{Timeout: 2 * time.Minute}}
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
	in := map[string]any{"model": c.cfg.Model, "messages": ms, "stream": false, "think": c.cfg.Think}
	if len(tools) > 0 {
		in["tools"] = tools
	}
	if c.cfg.NumCtx > 0 {
		in["options"] = map[string]any{"num_ctx": c.cfg.NumCtx}
	}
	var out struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
	}
	if err := c.post(ctx, strings.TrimRight(c.cfg.BaseURL, "/")+"/api/chat", in, &out); err != nil {
		return Response{}, err
	}
	return Response{Content: out.Message.Content, ToolCalls: out.Message.ToolCalls}, nil
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
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chat provider returned %s: %s", resp.Status, bb)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

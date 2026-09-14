package embeddings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
	Dimension() int
	Model() string
}

func New(provider, baseURL, apiKey, model string, dim int) (Embedder, error) {
	switch strings.ToLower(provider) {
	case "ollama":
		return &Ollama{baseURL: strings.TrimRight(baseURL, "/"), model: model, dim: dim, http: &http.Client{Timeout: 60 * time.Second}}, nil
	case "openai":
		return &OpenAI{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model, dim: dim, http: &http.Client{Timeout: 60 * time.Second}}, nil
	case "fake":
		return Fake{model: model, dim: dim}, nil
	default:
		return nil, fmt.Errorf("unknown EMBEDDINGS_PROVIDER %q", provider)
	}
}

type Ollama struct {
	baseURL, model string
	dim            int
	http           *http.Client
}

func (o *Ollama) Dimension() int { return o.dim }
func (o *Ollama) Model() string  { return o.model }
func (o *Ollama) Embed(ctx context.Context, text string) ([]float64, error) {
	in := map[string]string{"model": o.model, "prompt": text}
	var out struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := postJSON(ctx, o.http, o.baseURL+"/api/embeddings", "", in, &out); err != nil {
		return nil, err
	}
	return checkDim(out.Embedding, o.dim)
}

type OpenAI struct {
	baseURL, apiKey, model string
	dim                    int
	http                   *http.Client
}

func (o *OpenAI) Dimension() int { return o.dim }
func (o *OpenAI) Model() string  { return o.model }
func (o *OpenAI) Embed(ctx context.Context, text string) ([]float64, error) {
	in := map[string]any{"model": o.model, "input": text}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := postJSON(ctx, o.http, o.baseURL+"/embeddings", o.apiKey, in, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embedding response had no data")
	}
	return checkDim(out.Data[0].Embedding, o.dim)
}

type Fake struct {
	model string
	dim   int
}

func (f Fake) Dimension() int { return f.dim }
func (f Fake) Model() string  { return f.model }
func (f Fake) Embed(_ context.Context, text string) ([]float64, error) {
	v := make([]float64, f.dim)
	words := strings.Fields(strings.ToLower(text))
	for _, w := range words {
		h := sha256.Sum256([]byte(w))
		idx := int(binary.BigEndian.Uint32(h[:4]) % uint32(f.dim))
		v[idx] += 1
	}
	n := 0.0
	for _, x := range v {
		n += x * x
	}
	if n > 0 {
		n = math.Sqrt(n)
		for i := range v {
			v[i] /= n
		}
	}
	return v, nil
}

func checkDim(v []float64, want int) ([]float64, error) {
	if len(v) != want {
		return nil, fmt.Errorf("embedding dimension %d does not match configured dimension %d", len(v), want)
	}
	return v, nil
}
func postJSON(ctx context.Context, c *http.Client, url, key string, in, out any) error {
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

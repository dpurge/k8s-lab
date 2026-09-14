package qdrant

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"knowledge/internal/embeddings"
)

var ErrNotFound = errors.New("knowledge item not found")
var ErrValidation = errors.New("validation failed")

type Item struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Summary        string    `json:"summary"`
	Body           string    `json:"body,omitempty"`
	Tags           []string  `json:"tags"`
	EmbeddingModel string    `json:"embedding_model,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Score     *float64  `json:"score,omitempty"`
}

type Search struct {
	Query string
	Tags  []string
	Start *time.Time
	End   *time.Time
	Limit int
}

type Client struct {
	base, collection string
	http             *http.Client
	emb              embeddings.Embedder
}

func New(base, collection string, emb embeddings.Embedder) *Client {
	return &Client{base: strings.TrimRight(base, "/"), collection: collection, emb: emb, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/readyz", nil, nil)
}

func (c *Client) EnsureCollection(ctx context.Context) error {
	var info map[string]any
	if err := c.do(ctx, http.MethodGet, "/collections/"+c.collection, nil, &info); err == nil {
		got, ok := collectionDim(info)
		if ok && got != c.emb.Dimension() {
			return fmt.Errorf("collection %q vector size is %d, but %s requires %d; delete/recreate the collection or use a matching EMBEDDINGS_DIMENSION", c.collection, got, c.emb.Model(), c.emb.Dimension())
		}
		return c.ensureIndexes(ctx)
	}
	body := map[string]any{"vectors": map[string]any{"size": c.emb.Dimension(), "distance": "Cosine"}}
	if err := c.do(ctx, http.MethodPut, "/collections/"+c.collection, body, nil); err != nil {
		return err
	}
	return c.ensureIndexes(ctx)
}

func collectionDim(info map[string]any) (int, bool) {
	result, _ := info["result"].(map[string]any)
	config, _ := result["config"].(map[string]any)
	params, _ := config["params"].(map[string]any)
	vectors, _ := params["vectors"].(map[string]any)
	if size, ok := vectors["size"].(float64); ok {
		return int(size), true
	}
	return 0, false
}

func (c *Client) ensureIndexes(ctx context.Context) error {
	for _, idx := range []map[string]any{{"field_name": "tags", "field_schema": "keyword"}, {"field_name": "updated_at", "field_schema": "datetime"}} {
		_ = c.do(ctx, http.MethodPut, "/collections/"+c.collection+"/index", idx, nil)
	}
	return nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func validate(title, summary, body string) error {
	if strings.TrimSpace(title) == "" || strings.TrimSpace(summary) == "" || strings.TrimSpace(body) == "" {
		return ErrValidation
	}
	return nil
}
func NormalizeTags(tags []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
func embeddingText(title, summary, body string) string {
	return "Title: " + title + "\n\nSummary: " + summary + "\n\nBody:\n" + body
}

func (c *Client) Create(ctx context.Context, title, summary, body string, tags []string) (Item, error) {
	if err := validate(title, summary, body); err != nil {
		return Item{}, err
	}
	id, err := newID()
	if err != nil {
		return Item{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	it := Item{ID: id, Title: title, Summary: summary, Body: body, Tags: NormalizeTags(tags), EmbeddingModel: c.emb.Model(), CreatedAt: now, UpdatedAt: now}
	return it, c.upsert(ctx, it)
}

func (c *Client) Update(ctx context.Context, id, title, summary, body string, tags []string) (Item, error) {
	old, err := c.Get(ctx, id)
	if err != nil {
		return Item{}, err
	}
	if err := validate(title, summary, body); err != nil {
		return Item{}, err
	}
	old.Title, old.Summary, old.Body, old.Tags, old.EmbeddingModel, old.UpdatedAt = title, summary, body, NormalizeTags(tags), c.emb.Model(), time.Now().UTC().Truncate(time.Second)
	return old, c.upsert(ctx, old)
}

func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/collections/"+c.collection+"/points/delete?wait=true", map[string]any{"points": []string{id}}, nil)
}

func (c *Client) Get(ctx context.Context, id string) (Item, error) {
	var res struct {
		Result []point `json:"result"`
	}
	err := c.do(ctx, http.MethodPost, "/collections/"+c.collection+"/points", map[string]any{"ids": []string{id}, "with_payload": true, "with_vector": false}, &res)
	if err != nil {
		return Item{}, err
	}
	if len(res.Result) == 0 {
		return Item{}, ErrNotFound
	}
	return res.Result[0].item()
}

func (c *Client) Search(ctx context.Context, s Search) ([]ListItem, error) {
	if s.Limit <= 0 {
		s.Limit = 7
	}
	if s.Limit > 20 {
		s.Limit = 20
	}
	s.Tags = NormalizeTags(s.Tags)
	if strings.TrimSpace(s.Query) != "" {
		return c.vectorSearch(ctx, s)
	}
	return c.scroll(ctx, s)
}

func (c *Client) vectorSearch(ctx context.Context, s Search) ([]ListItem, error) {
	vec, err := c.emb.Embed(ctx, s.Query)
	if err != nil {
		return nil, err
	}
	req := map[string]any{"vector": vec, "limit": s.Limit, "with_payload": true, "with_vector": false}
	if f := filter(s); f != nil {
		req["filter"] = f
	}
	var res struct {
		Result []point `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, "/collections/"+c.collection+"/points/search", req, &res); err != nil {
		return nil, err
	}
	out := make([]ListItem, 0, len(res.Result))
	for _, p := range res.Result {
		it, err := p.item()
		if err == nil {
			score := p.Score
			out = append(out, ListItem{ID: it.ID, Title: it.Title, Summary: it.Summary, Tags: it.Tags, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt, Score: &score})
		}
	}
	return out, nil
}

func (c *Client) scroll(ctx context.Context, s Search) ([]ListItem, error) {
	var all []ListItem
	offset := any(nil)
	for len(all) < s.Limit {
		req := map[string]any{"limit": s.Limit, "with_payload": true, "with_vector": false}
		if f := filter(s); f != nil {
			req["filter"] = f
		}
		if offset != nil {
			req["offset"] = offset
		}
		var res struct {
			Result struct {
				Points         []point `json:"points"`
				NextPageOffset any     `json:"next_page_offset"`
			} `json:"result"`
		}
		if err := c.do(ctx, http.MethodPost, "/collections/"+c.collection+"/points/scroll", req, &res); err != nil {
			return nil, err
		}
		for _, p := range res.Result.Points {
			it, err := p.item()
			if err == nil {
				all = append(all, ListItem{ID: it.ID, Title: it.Title, Summary: it.Summary, Tags: it.Tags, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt})
				if len(all) >= s.Limit {
					break
				}
			}
		}
		if res.Result.NextPageOffset == nil {
			break
		}
		offset = res.Result.NextPageOffset
	}
	return all, nil
}

func filter(s Search) map[string]any {
	must := []any{}
	for _, tag := range NormalizeTags(s.Tags) {
		must = append(must, map[string]any{"key": "tags", "match": map[string]any{"value": tag}})
	}
	if s.Start != nil || s.End != nil {
		r := map[string]any{}
		if s.Start != nil {
			r["gte"] = s.Start.Format(time.RFC3339)
		}
		if s.End != nil {
			r["lte"] = s.End.Format(time.RFC3339)
		}
		must = append(must, map[string]any{"key": "updated_at", "range": r})
	}
	if len(must) == 0 {
		return nil
	}
	return map[string]any{"must": must}
}

func (c *Client) upsert(ctx context.Context, it Item) error {
	vec, err := c.emb.Embed(ctx, embeddingText(it.Title, it.Summary, it.Body))
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, "/collections/"+c.collection+"/points?wait=true", map[string]any{"points": []any{map[string]any{"id": it.ID, "vector": vec, "payload": it}}}, nil)
}

type point struct {
	ID      string         `json:"id"`
	Score   float64        `json:"score"`
	Payload map[string]any `json:"payload"`
}

func (p point) item() (Item, error) {
	b, _ := json.Marshal(p.Payload)
	var it Item
	if err := json.Unmarshal(b, &it); err != nil {
		return Item{}, err
	}
	if it.ID == "" {
		it.ID = p.ID
	}
	if it.Tags == nil {
		it.Tags = []string{}
	}
	return it, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant %s %s: %s: %s", method, path, resp.Status, b)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

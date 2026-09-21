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
	ID             string    `json:"id" yaml:"id"`
	Title          string    `json:"title" yaml:"title"`
	Summary        string    `json:"summary" yaml:"summary"`
	Body           string    `json:"body,omitempty" yaml:"body,omitempty"`
	Tags           []string  `json:"tags" yaml:"tags"`
	EmbeddingModel string    `json:"embedding_model,omitempty" yaml:"embedding_model,omitempty"`
	CreatedAt      time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" yaml:"updated_at"`
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
	minScore         float64
}

func New(base, collection string, emb embeddings.Embedder, minScore float64) *Client {
	return &Client{base: strings.TrimRight(base, "/"), collection: collection, emb: emb, minScore: minScore, http: &http.Client{Timeout: 30 * time.Second}}
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

// ExportAll scrolls the entire collection (optionally filtered by tags) and
// returns full items, including body — unlike Search/scroll, which cap
// results for the UI and omit body from list responses.
func (c *Client) ExportAll(ctx context.Context, tags []string) ([]Item, error) {
	tags = NormalizeTags(tags)
	var all []Item
	var offset any
	for {
		req := map[string]any{"limit": 200, "with_payload": true, "with_vector": false}
		if len(tags) > 0 {
			req["filter"] = filter(Search{Tags: tags})
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
			if it, err := p.item(); err == nil {
				all = append(all, it)
			}
		}
		if res.Result.NextPageOffset == nil || len(res.Result.Points) == 0 {
			break
		}
		offset = res.Result.NextPageOffset
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	return all, nil
}

// ImportItem mirrors Item for bulk import. ID is optional (id-less rows
// create a new item). CreatedAt/UpdatedAt are deliberately absent — the
// roadmap calls them informational-only on reimport, so a stored value is
// never even a field this type can carry; the server always sets its own.
// Delete marks a row for removal instead of create/update: absent/false for
// every normal row.
type ImportItem struct {
	ID      string   `json:"id,omitempty" yaml:"id,omitempty"`
	Title   string   `json:"title" yaml:"title"`
	Summary string   `json:"summary" yaml:"summary"`
	Body    string   `json:"body" yaml:"body"`
	Tags    []string `json:"tags" yaml:"tags"`
	Delete  bool     `json:"delete,omitempty" yaml:"delete,omitempty"`
}
type ImportError struct {
	Index   int    `json:"index"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message"`
}
type ImportResult struct {
	Imported  int           `json:"imported"`
	Deleted   int           `json:"deleted"`
	Unchanged int           `json:"unchanged"`
	Errors    []ImportError `json:"errors"`
}

// unchanged reports whether old already matches everything in that would be
// written for in, given the currently-configured embedding model — the
// title/summary/body/tags the roadmap names, plus the model, since a model
// swap makes an identical-text item's stored vector stale even though the
// text itself didn't change.
func unchanged(old Item, in ImportItem, currentModel string) bool {
	return old.Title == in.Title &&
		old.Summary == in.Summary &&
		old.Body == in.Body &&
		equalTags(old.Tags, NormalizeTags(in.Tags)) &&
		old.EmbeddingModel == currentModel
}

func equalTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Import applies each row independently, collecting per-row errors rather
// than aborting the whole batch on the first bad row:
//   - Delete: hard-deletes by id (requires an id; a missing target is a
//     no-op, matching this app's other idempotent deletes).
//   - An id that already exists AND is unchanged (title/summary/body/tags/
//     embedding model all match what's stored): skipped entirely — no
//     write, no re-embed. Re-embedding is a real LLM/embedding cost, only
//     paid when content actually changed.
//   - Otherwise: created (id-less, or an id that doesn't exist yet) or
//     updated (an existing id with different content) — either way,
//     (re-)embedded via upsert. An update preserves the stored CreatedAt.
func (c *Client) Import(ctx context.Context, items []ImportItem) ImportResult {
	res := ImportResult{Errors: []ImportError{}}
	for i, in := range items {
		if in.Delete {
			if in.ID == "" {
				res.Errors = append(res.Errors, ImportError{Index: i, Message: "delete requires id"})
				continue
			}
			if err := c.Delete(ctx, in.ID); err != nil {
				res.Errors = append(res.Errors, ImportError{Index: i, ID: in.ID, Message: err.Error()})
				continue
			}
			res.Deleted++
			continue
		}

		if err := validate(in.Title, in.Summary, in.Body); err != nil {
			res.Errors = append(res.Errors, ImportError{Index: i, ID: in.ID, Message: "title, summary, and body are required"})
			continue
		}

		now := time.Now().UTC().Truncate(time.Second)
		createdAt := now

		id := in.ID
		if id == "" {
			var err error
			id, err = newID()
			if err != nil {
				res.Errors = append(res.Errors, ImportError{Index: i, Message: err.Error()})
				continue
			}
		} else if old, err := c.Get(ctx, id); err == nil {
			if unchanged(old, in, c.emb.Model()) {
				res.Unchanged++
				continue
			}
			createdAt = old.CreatedAt
		} else if !errors.Is(err, ErrNotFound) {
			res.Errors = append(res.Errors, ImportError{Index: i, ID: id, Message: err.Error()})
			continue
		}

		it := Item{ID: id, Title: in.Title, Summary: in.Summary, Body: in.Body, Tags: NormalizeTags(in.Tags), EmbeddingModel: c.emb.Model(), CreatedAt: createdAt, UpdatedAt: now}
		if err := c.upsert(ctx, it); err != nil {
			res.Errors = append(res.Errors, ImportError{Index: i, ID: id, Message: err.Error()})
			continue
		}
		res.Imported++
	}
	return res
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
		if err == nil && (c.minScore <= 0 || p.Score >= c.minScore) {
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

package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"knowledge/internal/embeddings"
)

// newTestClient stubs just enough of Qdrant's HTTP API (points get, points
// upsert) for Import/Create/Update to run against, backed by an in-memory
// map so a Get after an Import in the same test sees what was written.
func newTestClient(t *testing.T, existing map[string]Item) (*Client, *[]map[string]any) {
	t.Helper()
	var upserts []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/kb/points", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var req struct {
				IDs []string `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			var results []map[string]any
			for _, id := range req.IDs {
				if it, ok := existing[id]; ok {
					b, _ := json.Marshal(it)
					var payload map[string]any
					_ = json.Unmarshal(b, &payload)
					results = append(results, map[string]any{"id": id, "payload": payload})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": results})
		case http.MethodPut:
			var body struct {
				Points []map[string]any `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			upserts = append(upserts, body.Points...)
			for _, p := range body.Points {
				id, _ := p["id"].(string)
				payload, _ := p["payload"].(map[string]any)
				b, _ := json.Marshal(payload)
				var it Item
				_ = json.Unmarshal(b, &it)
				existing[id] = it
			}
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	emb, err := embeddings.New("fake", "", "", "test-model", 4)
	if err != nil {
		t.Fatalf("embeddings.New: %v", err)
	}
	return New(srv.URL, "kb", emb, 0), &upserts
}

func TestImport_OnlyBodyRequired(t *testing.T) {
	c, _ := newTestClient(t, map[string]Item{})
	res := c.Import(context.Background(), []ImportItem{{Body: "just a body, no title, no summary, no tags"}})
	if len(res.Errors) != 0 {
		t.Fatalf("expected no errors, got %+v", res.Errors)
	}
	if res.Imported != 1 {
		t.Fatalf("expected 1 imported, got %+v", res)
	}
}

func TestImport_EmptyBodyStillRejected(t *testing.T) {
	c, _ := newTestClient(t, map[string]Item{})
	res := c.Import(context.Background(), []ImportItem{{Title: "t", Summary: "s"}})
	if len(res.Errors) != 1 {
		t.Fatalf("expected exactly 1 error for missing body, got %+v", res.Errors)
	}
}

func TestImport_OmittedTitleOnExistingRowKeepsOldTitle(t *testing.T) {
	existing := map[string]Item{
		"id1": {ID: "id1", Title: "Old Title", Summary: "Old Summary", Body: "old body", EmbeddingModel: "test-model", Tags: []string{}},
	}
	c, upserts := newTestClient(t, existing)
	res := c.Import(context.Background(), []ImportItem{{ID: "id1", Body: "new body"}})
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	if res.Imported != 1 {
		t.Fatalf("expected imported=1, got %+v", res)
	}
	last := (*upserts)[len(*upserts)-1]
	payload := last["payload"].(map[string]any)
	if payload["title"] != "Old Title" {
		t.Fatalf("expected existing title 'Old Title' to be preserved when omitted on reimport, got %v", payload["title"])
	}
}

func TestCreate_StillRequiresTitleSummaryBody(t *testing.T) {
	c, _ := newTestClient(t, map[string]Item{})
	if _, err := c.Create(context.Background(), "", "summary", "body", nil); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for missing title on Create, got %v", err)
	}
}

type fakeGenerator struct {
	titleCalls, summaryCalls []string
}

func (f *fakeGenerator) EnqueueBackgroundTitle(_ context.Context, itemID, _ string) error {
	f.titleCalls = append(f.titleCalls, itemID)
	return nil
}
func (f *fakeGenerator) EnqueueBackgroundSummary(_ context.Context, itemID, _ string) error {
	f.summaryCalls = append(f.summaryCalls, itemID)
	return nil
}

func TestImport_MissingTitleAndSummaryQueuesBackgroundGenerate(t *testing.T) {
	c, _ := newTestClient(t, map[string]Item{})
	gen := &fakeGenerator{}
	c.SetGenerator(gen)
	res := c.Import(context.Background(), []ImportItem{{Body: "just a body"}})
	if res.Imported != 1 {
		t.Fatalf("expected imported=1, got %+v", res)
	}
	if len(gen.titleCalls) != 1 || len(gen.summaryCalls) != 1 {
		t.Fatalf("expected exactly one background title+summary enqueue, got title=%v summary=%v", gen.titleCalls, gen.summaryCalls)
	}
}

func TestImport_ProvidedTitleAndSummarySkipsBackgroundGenerate(t *testing.T) {
	c, _ := newTestClient(t, map[string]Item{})
	gen := &fakeGenerator{}
	c.SetGenerator(gen)
	res := c.Import(context.Background(), []ImportItem{{Title: "T", Summary: "S", Body: "body"}})
	if res.Imported != 1 {
		t.Fatalf("expected imported=1, got %+v", res)
	}
	if len(gen.titleCalls) != 0 || len(gen.summaryCalls) != 0 {
		t.Fatalf("expected no background generate calls when title/summary provided, got title=%v summary=%v", gen.titleCalls, gen.summaryCalls)
	}
}

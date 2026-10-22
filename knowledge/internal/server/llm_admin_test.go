package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"knowledge/internal/llmcalls"
	"knowledge/internal/modelrouter"
)

// fakeCalls records what the handlers ask and answers as scripted.
type fakeCalls struct {
	listFilter llmcalls.Filter
	listLimit  int
	listOffset int
	aggSince   time.Time
	aggGroup   string
	attempts   []llmcalls.Attempt
	aggregates []llmcalls.Aggregate
	err        error
	listCalled bool
	aggsCalled bool
}

func (f *fakeCalls) List(_ context.Context, fl llmcalls.Filter, limit, offset int) ([]llmcalls.Attempt, int, error) {
	f.listCalled, f.listFilter, f.listLimit, f.listOffset = true, fl, limit, offset
	return f.attempts, len(f.attempts), f.err
}

func (f *fakeCalls) Aggregates(_ context.Context, since time.Time, groupBy string) ([]llmcalls.Aggregate, error) {
	f.aggsCalled, f.aggSince, f.aggGroup = true, since, groupBy
	return f.aggregates, f.err
}

func get(h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestTheModelPagesNeedALogin(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, nil)
	s.SetLLMAdmin(&fakeCalls{}, func() []modelrouter.ModelInfo { return nil })
	for _, path := range []string{"/api/v1/admin/models", "/api/v1/admin/llm-calls", "/api/v1/admin/llm-aggregates"} {
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil)) // no cookie
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d, want 401", path, rec.Code)
		}
	}
}

func TestAdminModelsListsConfiguredIdsOnlyAndAnEmptyListIsAnArray(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, nil)
	rec := get(s.adminModels, "/api/v1/admin/models")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"models":[]}` {
		t.Errorf("no source: %d %q, want 200 and an empty array", rec.Code, rec.Body.String())
	}
	s.SetLLMAdmin(&fakeCalls{}, func() []modelrouter.ModelInfo {
		return []modelrouter.ModelInfo{
			{ID: "gemma", ProviderID: "ollama", Tags: []string{"default", "chat"}, Priority: 10, Available: true},
			{ID: "bge", ProviderID: "ollama-lan", Tags: []string{"embed"}, Priority: 100, EmbeddingSpace: "bge-m3", Available: false, UnavailableReason: "cooldown"},
		}
	})
	rec = get(s.adminModels, "/api/v1/admin/models")
	var body struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Models) != 2 {
		t.Fatalf("body = %s, err %v; want two models", rec.Body.String(), err)
	}
	allowed := map[string]bool{"id": true, "provider_id": true, "tags": true, "priority": true, "embedding_space": true, "available": true, "unavailable_reason": true}
	for _, m := range body.Models {
		for key := range m {
			if !allowed[key] {
				t.Errorf("model has the key %q, which may expose an address, a key or an upstream name", key)
			}
		}
	}
	if body.Models[0]["embedding_space"] != nil || body.Models[1]["embedding_space"] != "bge-m3" || body.Models[1]["unavailable_reason"] != "cooldown" {
		t.Errorf("models = %v, want the space only on the embedding model and the reason only when unavailable", body.Models)
	}
}

func TestAdminLLMCallsPassesFiltersAndPagingAndDefaults(t *testing.T) {
	f := &fakeCalls{attempts: []llmcalls.Attempt{{ID: "1", Kind: "chat", ModelID: "gemma", Tags: []string{"chat"}, Outcome: "ok"}}}
	s := New(nil, nil, nil, nil, nil, nil)
	s.SetLLMAdmin(f, nil)

	rec := get(s.adminLLMCalls, "/x")
	if rec.Code != 200 || f.listLimit != 25 || f.listOffset != 0 || f.listFilter != (llmcalls.Filter{}) {
		t.Errorf("defaults: %d, limit %d, offset %d, filter %+v; want 200, 25, 0 and no filter", rec.Code, f.listLimit, f.listOffset, f.listFilter)
	}
	var page struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Total != 1 || page.Limit != 25 {
		t.Errorf("page = %s, err %v", rec.Body.String(), err)
	}

	get(s.adminLLMCalls, "/x?model_id=gemma&outcome=error&kind=translate&since=2026-10-20T12:00:00Z&limit=50&offset=100")
	if f.listFilter.ModelID != "gemma" || f.listFilter.Outcome != "error" || f.listFilter.Kind != "translate" || f.listLimit != 50 || f.listOffset != 100 ||
		!f.listFilter.Since.Equal(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("filter %+v limit %d offset %d, want what the query string said", f.listFilter, f.listLimit, f.listOffset)
	}

	f2 := &fakeCalls{} // no attempts: the JSON must still hold an array, not null
	s.SetLLMAdmin(f2, nil)
	if body := get(s.adminLLMCalls, "/x").Body.String(); !strings.Contains(body, `"items":[]`) {
		t.Errorf("an empty page = %s, want an empty items array", body)
	}
	if body := get(s.adminLLMAggregates, "/x").Body.String(); !strings.Contains(body, `"items":[]`) {
		t.Errorf("empty aggregates = %s, want an empty items array", body)
	}
}

func TestAdminLLMCallsRejectsBadParametersWithoutQuerying(t *testing.T) {
	f := &fakeCalls{}
	s := New(nil, nil, nil, nil, nil, nil)
	s.SetLLMAdmin(f, nil)
	for _, q := range []string{"limit=0", "limit=201", "limit=abc", "offset=-1", "offset=x", "outcome=weird", "since=yesterday"} {
		rec := get(s.adminLLMCalls, "/x?"+q)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "validation_error") {
			t.Errorf("?%s = %d %s, want 400 validation_error", q, rec.Code, rec.Body.String())
		}
	}
	if f.listCalled {
		t.Error("the store was queried despite a bad parameter")
	}
}

func TestAdminLLMAggregatesDefaultsAndValidation(t *testing.T) {
	f := &fakeCalls{aggregates: []llmcalls.Aggregate{{ModelID: "gemma", ProviderID: "ollama", Calls: 3, OKCalls: 3, SuccessRate: 1}}}
	s := New(nil, nil, nil, nil, nil, nil)
	s.SetLLMAdmin(f, nil)

	before := time.Now()
	rec := get(s.adminLLMAggregates, "/x")
	if rec.Code != 200 || f.aggGroup != "model" {
		t.Fatalf("defaults: %d, group %q; want 200 and model", rec.Code, f.aggGroup)
	}
	if window := before.Sub(f.aggSince); window < 23*time.Hour || window > 25*time.Hour {
		t.Errorf("default window = %v, want about 24 hours", window)
	}
	get(s.adminLLMAggregates, "/x?group_by=kind_model&since=2026-10-01T00:00:00Z")
	if f.aggGroup != "kind_model" || !f.aggSince.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("group %q since %v, want kind_model and the given time", f.aggGroup, f.aggSince)
	}
	f.aggsCalled = false
	for _, q := range []string{"group_by=user", "since=nope"} {
		if rec := get(s.adminLLMAggregates, "/x?"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400", q, rec.Code)
		}
	}
	if f.aggsCalled {
		t.Error("the store was queried despite a bad parameter")
	}
}

func TestAStoreFailureOrNoStoreNeverLeaksDetails(t *testing.T) {
	f := &fakeCalls{err: errors.New(`dial tcp secret-db-host.internal:5432: connection refused`)}
	s := New(nil, nil, nil, nil, nil, nil)
	s.SetLLMAdmin(f, nil)
	for name, h := range map[string]http.HandlerFunc{"calls": s.adminLLMCalls, "aggregates": s.adminLLMAggregates} {
		rec := get(h, "/x")
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret-db-host") {
			t.Errorf("%s: %d %s, want a 500 that does not repeat the error", name, rec.Code, rec.Body.String())
		}
	}
	empty := New(nil, nil, nil, nil, nil, nil)
	for name, h := range map[string]http.HandlerFunc{"calls": empty.adminLLMCalls, "aggregates": empty.adminLLMAggregates} {
		if rec := get(h, "/x"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without a store = %d, want 503", name, rec.Code)
		}
	}
}

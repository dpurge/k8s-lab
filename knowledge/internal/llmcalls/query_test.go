package llmcalls_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"knowledge/internal/llmcalls"
)

func TestRedactAddressesHidesEveryAddressInAnErrorText(t *testing.T) {
	for in, want := range map[string]string{
		`dial tcp 192.168.1.5:11434: connect: connection refused`:                          `dial tcp [address]: connect: connection refused`,
		`dial tcp: lookup secret-host.lan on 127.0.0.53:53: no such host`:                  `dial tcp: lookup [address] on [address]: no such host`,
		`read tcp 10.0.0.1:5555->10.0.0.2:11434: read: connection reset by peer`:           `read tcp [address]->[address]: read: connection reset by peer`,
		`dial tcp host.docker.internal:11434: i/o timeout`:                                 `dial tcp [address]: i/o timeout`,
		`dial tcp [::1]:11434: connect: connection refused`:                                `dial tcp [address]: connect: connection refused`,
		`dial tcp localhost:11434: connect: connection refused`:                            `dial tcp [address]: connect: connection refused`,
		`ollama.data.svc.cluster.local:11434 refused`:                                      `[address] refused`,
		`started 2026-10-22T10:00:00Z, version 1.2.3, HTTP 503: overloaded (status:503)`:   `started 2026-10-22T10:00:00Z, version 1.2.3, HTTP 503: overloaded (status:503)`,
		`Post "http://host.docker.internal:11434/api/embed": dial tcp: connection refused`: `Post "[address]": dial tcp: connection refused`,
		`ollama: HTTP 404: model "x" not found`:                                            `ollama: HTTP 404: model "x" not found`,
		`got https://openrouter.ai/api/v1/embeddings (status 500) and http://10.0.0.5/x`:   `got [address] (status 500) and [address]`,
		``: ``,
	} {
		if got := llmcalls.RedactAddresses(in); got != want {
			t.Errorf("RedactAddresses(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnAttemptHasOnlyConfiguredIdsNumbersAndOutcome(t *testing.T) {
	b, err := json.Marshal(llmcalls.Attempt{Tags: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	_ = json.Unmarshal(b, &keys)
	want := []string{"id", "started_at", "kind", "operation_id", "model_id", "provider_id", "tags", "input_tokens", "output_tokens", "cost_usd", "duration_ms", "outcome", "error", "http_status", "request_id"}
	if len(keys) != len(want) {
		t.Errorf("keys = %v, want exactly %v", keys, want)
	}
	for _, k := range want {
		if _, ok := keys[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if _, ok := keys["model_name"]; ok {
		t.Error("model_name (the provider's own name for the model) must not be in the page's data")
	}
}

func insertCall(t *testing.T, s *llmcalls.Store, c llmcalls.Call) {
	t.Helper()
	if c.ProviderID == "" {
		c.ProviderID = "ollama"
	}
	if c.ModelName == "" {
		c.ModelName = "upstream-name"
	}
	if c.Outcome == "" {
		c.Outcome = "ok"
	}
	if err := s.Insert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestListFiltersPagesAndOrdersNewestFirstAndRedactsAddresses(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool)
	op := "11111111-2222-3333-4444-555555555555"
	boom := `Post "http://host.docker.internal:11434/api/embed": dial tcp: connection refused`
	insertCall(t, store, llmcalls.Call{Kind: "chat", ModelID: "gemma", Tags: []string{"chat"}, StartedAt: daysAgo(3), InputTokens: 5, OutputTokens: 2, DurationMS: 100, OperationID: &op})
	insertCall(t, store, llmcalls.Call{Kind: "embed", ModelID: "bge", Tags: []string{"embedding_space=bge-m3"}, StartedAt: daysAgo(2), DurationMS: 50, Outcome: "error", Error: &boom})
	insertCall(t, store, llmcalls.Call{Kind: "generate_title", ModelID: "gemma", StartedAt: daysAgo(1), DurationMS: 70})

	all, total, err := store.List(context.Background(), llmcalls.Filter{}, 10, 0)
	if err != nil || total != 3 || len(all) != 3 {
		t.Fatalf("List = %d rows of %d, %v; want 3 of 3", len(all), total, err)
	}
	if all[0].Kind != "generate_title" || all[2].Kind != "chat" {
		t.Errorf("order = %s, %s, %s; want newest first", all[0].Kind, all[1].Kind, all[2].Kind)
	}
	if all[2].OperationID == nil || *all[2].OperationID != op || all[1].OperationID != nil {
		t.Errorf("operation ids = %v / %v, want the chat's operation and none for the embed", all[2].OperationID, all[1].OperationID)
	}
	if all[0].Tags == nil {
		t.Error("an empty tag list must come back as an empty slice, not nil")
	}
	if all[1].Error == nil || strings.Contains(*all[1].Error, "host.docker.internal") || !strings.Contains(*all[1].Error, "[address]") {
		t.Errorf("error = %v, want the address redacted", all[1].Error)
	}

	cases := []struct {
		name  string
		f     llmcalls.Filter
		total int
	}{
		{"by model", llmcalls.Filter{ModelID: "gemma"}, 2},
		{"by outcome", llmcalls.Filter{Outcome: "error"}, 1},
		{"by kind", llmcalls.Filter{Kind: "chat"}, 1},
		{"since", llmcalls.Filter{Since: daysAgo(2).Add(-time.Hour)}, 2},
		{"all filters", llmcalls.Filter{ModelID: "gemma", Kind: "chat", Outcome: "ok", Since: daysAgo(4)}, 1},
		{"none match", llmcalls.Filter{ModelID: "nope"}, 0},
	}
	for _, c := range cases {
		got, total, err := store.List(context.Background(), c.f, 10, 0)
		if err != nil || total != c.total || len(got) != c.total {
			t.Errorf("%s: %d rows of %d, %v; want %d", c.name, len(got), total, err, c.total)
		}
	}
	page, total, err := store.List(context.Background(), llmcalls.Filter{}, 1, 1)
	if err != nil || total != 3 || len(page) != 1 || page[0].Kind != "embed" {
		t.Errorf("page 2 of size 1 = %v (total %d), %v; want the middle row and the full total", page, total, err)
	}
}

func TestAggregatesSummarizePerModelAndPerKind(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool)
	for i, d := range []int{1000, 2000, 3000} { // three successful chat calls, 10 output tokens each
		insertCall(t, store, llmcalls.Call{Kind: "chat", ModelID: "gemma", StartedAt: daysAgo(1).Add(time.Duration(i) * time.Minute), DurationMS: d, InputTokens: 100, OutputTokens: 10, CostUSD: 0.5})
	}
	insertCall(t, store, llmcalls.Call{Kind: "chat", ModelID: "gemma", StartedAt: daysAgo(1), DurationMS: 9000, Outcome: "timeout"})
	insertCall(t, store, llmcalls.Call{Kind: "generate_title", ModelID: "gemma", StartedAt: daysAgo(1), DurationMS: 500, OutputTokens: 5})
	insertCall(t, store, llmcalls.Call{Kind: "embed", ModelID: "bge", StartedAt: daysAgo(1), DurationMS: 100, InputTokens: 6})
	insertCall(t, store, llmcalls.Call{Kind: "chat", ModelID: "gemma", StartedAt: daysAgo(10), DurationMS: 1}) // outside the window

	byModel, err := store.Aggregates(context.Background(), daysAgo(2), llmcalls.GroupByModel)
	if err != nil || len(byModel) != 2 {
		t.Fatalf("by model = %+v, %v; want gemma and bge", byModel, err)
	}
	bge, gemma := byModel[0], byModel[1] // sorted by model id
	if bge.ModelID != "bge" || gemma.ModelID != "gemma" {
		t.Fatalf("order = %s, %s", bge.ModelID, gemma.ModelID)
	}
	if gemma.Calls != 5 || gemma.OKCalls != 4 || gemma.TimeoutCalls != 1 || gemma.SuccessRate != 0.8 || gemma.InputTokens != 300 || gemma.OutputTokens != 35 || gemma.CostUSD != 1.5 {
		t.Errorf("gemma = %+v, want 5 calls, 4 ok, 1 timeout, 80%%, 300/35 tokens, $1.5", gemma)
	}
	// Durations count successful attempts only: 1000, 2000, 3000 and the title's 500.
	if gemma.MeanDurationMS == nil || *gemma.MeanDurationMS != 1625 || gemma.P50DurationMS == nil || *gemma.P50DurationMS != 1500 {
		t.Errorf("gemma mean/p50 = %v/%v, want 1625/1500 (the timeout's 9000 ms is not counted)", gemma.MeanDurationMS, gemma.P50DurationMS)
	}
	if gemma.OutputTokensPerSecond == nil || *gemma.OutputTokensPerSecond < 5.3 || *gemma.OutputTokensPerSecond > 5.4 {
		t.Errorf("tokens per second = %v, want 35 tokens over 6.5 s", gemma.OutputTokensPerSecond)
	}
	if bge.OutputTokensPerSecond != nil || bge.Kind != nil {
		t.Errorf("bge = %+v, want no speed (no output tokens) and no kind when grouped by model", bge)
	}

	byKind, err := store.Aggregates(context.Background(), daysAgo(2), llmcalls.GroupByKindModel)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, a := range byKind {
		if a.Kind == nil {
			t.Fatalf("%+v has no kind when grouped by kind", a)
		}
		got[a.ModelID+"/"+*a.Kind] = a.Calls
	}
	want := map[string]int64{"bge/embed": 1, "gemma/chat": 4, "gemma/generate_title": 1}
	if len(got) != len(want) {
		t.Errorf("groups = %v, want %v (the call outside the window is not counted)", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("group %s has %d calls, want %d (all groups: %v)", k, got[k], n, got)
		}
	}
	if _, err := store.Aggregates(context.Background(), daysAgo(2), "user"); err == nil {
		t.Error("an unknown grouping must be refused")
	}
}

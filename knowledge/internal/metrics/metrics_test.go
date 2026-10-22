package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(m *Metrics) string {
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func TestObserveLLMCountsCallsTokensCostAndDuration(t *testing.T) {
	m := New()
	m.ObserveLLM("ollama", "gemma", "ok", 100, 20, 0.25, 1500*time.Millisecond)
	m.ObserveLLM("ollama", "gemma", "error", 0, 0, 0, 50*time.Millisecond)
	body := scrape(m)
	for _, want := range []string{
		`knowledge_llm_calls_total{model="gemma",outcome="ok",provider="ollama"} 1`,
		`knowledge_llm_calls_total{model="gemma",outcome="error",provider="ollama"} 1`,
		`knowledge_llm_tokens_total{direction="input",model="gemma",provider="ollama"} 100`,
		`knowledge_llm_tokens_total{direction="output",model="gemma",provider="ollama"} 20`,
		`knowledge_llm_cost_usd_total{model="gemma",provider="ollama"} 0.25`,
		`knowledge_llm_call_duration_seconds_count{model="gemma",provider="ollama"} 2`,
		`# TYPE knowledge_llm_record_errors_total counter`,
		`# TYPE knowledge_llm_calls_purged_total counter`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the metrics lack %q", want)
		}
	}
}

func TestAFailedAttemptThatUsedNoTokensAddsNoTokenOrCostSeries(t *testing.T) {
	m := New()
	m.ObserveLLM("ollama", "gemma", "timeout", 0, 0, 0, time.Second)
	body := scrape(m)
	if strings.Contains(body, "knowledge_llm_tokens_total{") || strings.Contains(body, "knowledge_llm_cost_usd_total{") {
		t.Errorf("token or cost series exist for an attempt that used none:\n%s", body)
	}
}

func TestRecordErrorsAndPurgesAreCountedAndOnlyTheLLMRuleCountsAsPurged(t *testing.T) {
	m := New()
	m.RecordError()
	m.RecordError()
	m.Purged(RuleLLMCalls, 7)
	m.Purged("another_rule", 99)
	body := scrape(m)
	if !strings.Contains(body, "knowledge_llm_record_errors_total 2\n") || !strings.Contains(body, "knowledge_llm_calls_purged_total 7\n") {
		t.Errorf("record errors or purges not counted as expected:\n%s", body)
	}
}

func TestTheAvailabilityGaugeIsReadAtScrapeTime(t *testing.T) {
	m := New()
	if strings.Contains(scrape(m), "knowledge_llm_model_available{") {
		t.Error("no model series before the source is set")
	}
	state := true
	m.SetModelAvailability(func() []ModelAvailability {
		return []ModelAvailability{{Model: "a", Available: state}, {Model: "b", Available: false}}
	})
	body := scrape(m)
	if !strings.Contains(body, `knowledge_llm_model_available{model="a"} 1`) || !strings.Contains(body, `knowledge_llm_model_available{model="b"} 0`) {
		t.Errorf("gauge wrong:\n%s", body)
	}
	state = false
	if !strings.Contains(scrape(m), `knowledge_llm_model_available{model="a"} 0`) {
		t.Error("the gauge must follow the state at every scrape")
	}
}

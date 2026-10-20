package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func wantLine(t *testing.T, body, line string) {
	t.Helper()
	if !strings.Contains(body, line+"\n") {
		t.Errorf("missing line %q in:\n%s", line, body)
	}
}

func TestObserveLLM(t *testing.T) {
	m := New()
	m.ObserveLLM("gpu-box", "big", "ok", 1200, 300, 0.0081, 2*time.Second)
	m.ObserveLLM("gpu-box", "big", "ok", 800, 200, 0.0054, 3*time.Second)
	m.ObserveLLM("laptop", "small", "ok", 50, 5, 0, time.Second)
	m.ObserveLLM("gpu-box", "big", "timeout", 0, 0, 0, 5*time.Second)
	body := scrape(t, m)
	wantLine(t, body, `phraseforge_llm_calls_total{model="big",outcome="ok",provider="gpu-box"} 2`)
	wantLine(t, body, `phraseforge_llm_calls_total{model="big",outcome="timeout",provider="gpu-box"} 1`)
	wantLine(t, body, `phraseforge_llm_tokens_total{direction="input",model="big",provider="gpu-box"} 2000`)
	wantLine(t, body, `phraseforge_llm_tokens_total{direction="output",model="big",provider="gpu-box"} 500`)
	wantLine(t, body, `phraseforge_llm_tokens_total{direction="input",model="small",provider="laptop"} 50`)
	wantLine(t, body, `phraseforge_llm_cost_usd_total{model="big",provider="gpu-box"} 0.0135`)
	wantLine(t, body, `phraseforge_llm_call_duration_seconds_count{model="big",provider="gpu-box"} 3`)
	wantLine(t, body, `phraseforge_llm_call_duration_seconds_sum{model="big",provider="gpu-box"} 10`)
	wantLine(t, body, `phraseforge_llm_call_duration_seconds_bucket{model="big",provider="gpu-box",le="1800"} 3`)
}

func TestAFreeOrFailedCallAddsNoTokenOrCostSeries(t *testing.T) {
	m := New()
	m.ObserveLLM("laptop", "small", "timeout", 0, 0, 0, time.Second)
	body := scrape(t, m)
	if strings.Contains(body, "phraseforge_llm_tokens_total{") || strings.Contains(body, "phraseforge_llm_cost_usd_total{") {
		t.Errorf("no tokens or cost, no series:\n%s", body)
	}
}

func TestARecordErrorIsCounted(t *testing.T) {
	m := New()
	m.RecordError()
	m.RecordError()
	wantLine(t, scrape(t, m), `phraseforge_llm_record_errors_total 2`)
}

func TestEachMetricsInstanceHasItsOwnRegistry(t *testing.T) {
	a, b := New(), New()
	a.ObserveLLM("p", "m", "ok", 0, 0, 0, 0)
	if strings.Contains(scrape(t, b), `phraseforge_llm_calls_total{`) {
		t.Errorf("a second instance must start empty")
	}
}

func TestModelAvailabilityIsReadAtEveryScrape(t *testing.T) {
	m := New()
	if strings.Contains(scrape(t, m), "phraseforge_llm_model_available{") {
		t.Errorf("nothing registered yet, nothing exposed")
	}
	up := true
	m.SetModelAvailability(func() []ModelAvailability {
		return []ModelAvailability{{Model: "big", Available: up}, {Model: "small", Available: true}}
	})
	body := scrape(t, m)
	wantLine(t, body, `phraseforge_llm_model_available{model="big"} 1`)
	up = false
	body = scrape(t, m)
	wantLine(t, body, `phraseforge_llm_model_available{model="big"} 0`)
	wantLine(t, body, `phraseforge_llm_model_available{model="small"} 1`)
}

func TestPurgedCountsEachRuleInItsOwnSeriesAndIgnoresUnknownOnes(t *testing.T) {
	m := New()
	body := scrape(t, m)
	wantLine(t, body, `phraseforge_jobs_purged_total{rule="jobs_done"} 0`)
	wantLine(t, body, `phraseforge_jobs_purged_total{rule="jobs_failed"} 0`)
	m.Purged("llm_calls", 4)
	m.Purged("jobs_done", 2)
	m.Purged("jobs_done", 1)
	m.Purged("jobs_failed", 5)
	m.Purged("something_else", 9)
	body = scrape(t, m)
	wantLine(t, body, "phraseforge_llm_calls_purged_total 4")
	wantLine(t, body, `phraseforge_jobs_purged_total{rule="jobs_done"} 3`)
	wantLine(t, body, `phraseforge_jobs_purged_total{rule="jobs_failed"} 5`)
	if strings.Contains(body, "something_else") {
		t.Errorf("an unknown rule became a label value:\n%s", body)
	}
}

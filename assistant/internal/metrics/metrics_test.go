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
		t.Errorf("missing line %q in:\n%s", line, grep(body, strings.SplitN(line, "{", 2)[0]))
	}
}

func grep(body, prefix string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestNewMetricsExposeGoAndProcessCollectors(t *testing.T) {
	body := scrape(t, New())
	for _, name := range []string{"go_goroutines", "process_cpu_seconds_total"} {
		if !strings.Contains(body, name) {
			t.Errorf("%s not exposed", name)
		}
	}
}

func TestObserveHTTP(t *testing.T) {
	m := New()
	m.ObserveHTTP("GET", "/api/v1/tasks/{id}", 200, 20*time.Millisecond)
	m.ObserveHTTP("GET", "/api/v1/tasks/{id}", 200, 30*time.Millisecond)
	m.ObserveHTTP("POST", "/api/v1/tasks", 201, 5*time.Millisecond)
	body := scrape(t, m)
	wantLine(t, body, `assistant_http_requests_total{method="GET",route="/api/v1/tasks/{id}",status="200"} 2`)
	wantLine(t, body, `assistant_http_requests_total{method="POST",route="/api/v1/tasks",status="201"} 1`)
	wantLine(t, body, `assistant_http_request_duration_seconds_count{method="GET",route="/api/v1/tasks/{id}"} 2`)
	if !strings.Contains(body, `assistant_http_request_duration_seconds_sum{method="GET",route="/api/v1/tasks/{id}"} 0.05`) {
		t.Errorf("duration sum wrong:\n%s", grep(body, "assistant_http_request_duration_seconds_sum"))
	}
}

func TestObserveTask(t *testing.T) {
	m := New()
	m.ObserveTask("api", "noop", "succeeded", 2*time.Second, 150*time.Millisecond)
	m.ObserveTask("api", "noop", "failed", 4*time.Second, 50*time.Millisecond)
	m.ObserveTask("telegram", "learn-topic", "succeeded", time.Second, time.Minute)
	body := scrape(t, m)
	wantLine(t, body, `assistant_tasks_total{source="api",status="succeeded",workflow="noop"} 1`)
	wantLine(t, body, `assistant_tasks_total{source="api",status="failed",workflow="noop"} 1`)
	wantLine(t, body, `assistant_tasks_total{source="telegram",status="succeeded",workflow="learn-topic"} 1`)
	wantLine(t, body, `assistant_task_queue_seconds_count{workflow="noop"} 2`)
	wantLine(t, body, `assistant_task_queue_seconds_sum{workflow="noop"} 6`)
	wantLine(t, body, `assistant_task_execution_seconds_count{workflow="noop"} 2`)
	wantLine(t, body, `assistant_task_execution_seconds_sum{workflow="noop"} 0.2`)
	wantLine(t, body, `assistant_task_execution_seconds_sum{workflow="learn-topic"} 60`)
}

func TestEachMetricsInstanceHasItsOwnRegistry(t *testing.T) {
	a, b := New(), New()
	a.ObserveTask("api", "noop", "succeeded", 0, 0)
	if strings.Contains(scrape(t, b), `assistant_tasks_total{`) {
		t.Errorf("a second instance must start empty")
	}
}

func TestObserveLLM(t *testing.T) {
	m := New()
	m.ObserveLLM("u1", "gpu-box", "big", "ok", 1200, 300, 0.0081, 2*time.Second)
	m.ObserveLLM("u1", "gpu-box", "big", "ok", 800, 200, 0.0054, 3*time.Second)
	m.ObserveLLM("u2", "laptop", "small", "ok", 50, 5, 0, time.Second)
	m.ObserveLLM("u1", "gpu-box", "big", "timeout", 0, 0, 0, 5*time.Second)
	body := scrape(t, m)
	wantLine(t, body, `assistant_llm_calls_total{model="big",outcome="ok",provider="gpu-box"} 2`)
	wantLine(t, body, `assistant_llm_calls_total{model="big",outcome="timeout",provider="gpu-box"} 1`)
	wantLine(t, body, `assistant_llm_calls_total{model="small",outcome="ok",provider="laptop"} 1`)
	wantLine(t, body, `assistant_llm_tokens_total{direction="input",model="big",provider="gpu-box",user="u1"} 2000`)
	wantLine(t, body, `assistant_llm_tokens_total{direction="output",model="big",provider="gpu-box",user="u1"} 500`)
	wantLine(t, body, `assistant_llm_tokens_total{direction="input",model="small",provider="laptop",user="u2"} 50`)
	wantLine(t, body, `assistant_llm_cost_usd_total{model="big",provider="gpu-box",user="u1"} 0.0135`)
	wantLine(t, body, `assistant_llm_call_duration_seconds_count{model="big",provider="gpu-box"} 3`)
	wantLine(t, body, `assistant_llm_call_duration_seconds_sum{model="big",provider="gpu-box"} 10`)
}

func TestAFreeOrFailedCallAddsNoTokenOrCostSeries(t *testing.T) {
	m := New()
	m.ObserveLLM("u1", "laptop", "small", "timeout", 0, 0, 0, time.Second)
	body := scrape(t, m)
	if strings.Contains(body, "assistant_llm_tokens_total{") || strings.Contains(body, "assistant_llm_cost_usd_total{") {
		t.Errorf("no tokens or cost, no series:\n%s", grep(body, "assistant_llm"))
	}
	wantLine(t, body, `assistant_llm_calls_total{model="small",outcome="timeout",provider="laptop"} 1`)
}

func TestARecordErrorIsCounted(t *testing.T) {
	m := New()
	m.RecordError()
	m.RecordError()
	wantLine(t, scrape(t, m), `assistant_llm_record_errors_total 2`)
}

func TestModelAvailabilityIsReadAtEveryScrape(t *testing.T) {
	m := New()
	if strings.Contains(scrape(t, m), "assistant_llm_model_available{") {
		t.Errorf("nothing registered yet, nothing exposed")
	}
	up := true
	m.SetModelAvailability(func() []ModelAvailability {
		return []ModelAvailability{{Model: "big", Available: up}, {Model: "small", Available: true}}
	})
	body := scrape(t, m)
	wantLine(t, body, `assistant_llm_model_available{model="big"} 1`)
	wantLine(t, body, `assistant_llm_model_available{model="small"} 1`)
	up = false
	body = scrape(t, m)
	wantLine(t, body, `assistant_llm_model_available{model="big"} 0`)
	wantLine(t, body, `assistant_llm_model_available{model="small"} 1`)
}

func TestModelAvailabilityCanBeReplaced(t *testing.T) {
	m := New()
	m.SetModelAvailability(func() []ModelAvailability { return []ModelAvailability{{Model: "old", Available: true}} })
	m.SetModelAvailability(func() []ModelAvailability { return []ModelAvailability{{Model: "new", Available: false}} })
	body := scrape(t, m)
	if strings.Contains(body, `model="old"`) {
		t.Errorf("the old function must be gone")
	}
	wantLine(t, body, `assistant_llm_model_available{model="new"} 0`)
}

func TestATraceRecordErrorIsCounted(t *testing.T) {
	m := New()
	m.TraceRecordError()
	wantLine(t, scrape(t, m), `assistant_trace_record_errors_total 1`)
}

func TestPurgedRowsAreCounted(t *testing.T) {
	m := New()
	wantLine(t, scrape(t, m), "assistant_llm_calls_purged_total 0")
	m.Purged("llm_calls", 3)
	m.Purged("llm_calls", 4)
	wantLine(t, scrape(t, m), "assistant_llm_calls_purged_total 7")
}

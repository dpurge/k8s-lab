// Package metrics holds phraseforge's Prometheus collectors. They live in a
// registry of their own rather than the global default, so every test builds a
// fresh one and nothing registers twice. Labels are the configured provider and
// model ids only: never a kind, a language, a job or an error text, so the
// number of series stays bounded.
package metrics

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// llmBuckets suit model calls, from a quick answer to the half-hour a large
// local model may take.
var llmBuckets = []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1200, 1800}

// Metrics is the set of collectors, with their registry.
type Metrics struct {
	registry *prometheus.Registry

	llmCalls        *prometheus.CounterVec
	llmTokens       *prometheus.CounterVec
	llmCost         *prometheus.CounterVec
	llmDuration     *prometheus.HistogramVec
	llmRecordErrors prometheus.Counter
	llmPurged       prometheus.Counter
	jobsPurged      *prometheus.CounterVec
	availability    *availabilityCollector
}

// New returns collectors registered in a new registry, together with the Go
// runtime and process collectors.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		llmCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "phraseforge_llm_calls_total",
			Help: "Attempts to call a model, by provider, model and outcome.",
		}, []string{"provider", "model", "outcome"}),
		llmTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "phraseforge_llm_tokens_total",
			Help: "Tokens used by model calls, by provider, model and direction (input or output).",
		}, []string{"provider", "model", "direction"}),
		llmCost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "phraseforge_llm_cost_usd_total",
			Help: "Cost of model calls in US dollars, by provider and model.",
		}, []string{"provider", "model"}),
		llmDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "phraseforge_llm_call_duration_seconds",
			Help:    "Duration of model call attempts in seconds, by provider and model.",
			Buckets: llmBuckets,
		}, []string{"provider", "model"}),
		llmRecordErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "phraseforge_llm_record_errors_total",
			Help: "Model calls that could not be recorded in the database.",
		}),
		llmPurged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "phraseforge_llm_calls_purged_total",
			Help: "Model call records deleted by the retention purge.",
		}),
		jobsPurged: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "phraseforge_jobs_purged_total",
			Help: "Finished jobs deleted by the retention purge, by rule (jobs_done, jobs_failed).",
		}, []string{"rule"}),
		availability: newAvailabilityCollector(),
	}
	// Both series exist from the start so that rate() and absent() work before
	// the first purge.
	m.jobsPurged.WithLabelValues(RuleJobsDone)
	m.jobsPurged.WithLabelValues(RuleJobsFailed)
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.llmCalls, m.llmTokens, m.llmCost, m.llmDuration, m.llmRecordErrors, m.llmPurged, m.jobsPurged, m.availability,
	)
	return m
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveLLM records one attempt to call a model. Tokens and cost are counted
// for every attempt; a failed attempt simply has none.
func (m *Metrics) ObserveLLM(provider, model, outcome string, inputTokens, outputTokens int, costUSD float64, elapsed time.Duration) {
	m.llmCalls.WithLabelValues(provider, model, outcome).Inc()
	m.llmDuration.WithLabelValues(provider, model).Observe(elapsed.Seconds())
	if inputTokens > 0 {
		m.llmTokens.WithLabelValues(provider, model, "input").Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		m.llmTokens.WithLabelValues(provider, model, "output").Add(float64(outputTokens))
	}
	if costUSD > 0 {
		m.llmCost.WithLabelValues(provider, model).Add(costUSD)
	}
}

// RecordError counts a model call that could not be written to the database.
func (m *Metrics) RecordError() { m.llmRecordErrors.Inc() }

// Retention rule names whose purges are counted; the rule label of
// phraseforge_jobs_purged_total only ever takes the jobs ones.
const (
	RuleLLMCalls   = "llm_calls"
	RuleJobsDone   = "jobs_done"
	RuleJobsFailed = "jobs_failed"
)

// Purged counts rows a retention rule deleted. A rule it does not know is
// ignored, which keeps the rule label bounded.
func (m *Metrics) Purged(rule string, rows int64) {
	switch rule {
	case RuleLLMCalls:
		m.llmPurged.Add(float64(rows))
	case RuleJobsDone, RuleJobsFailed:
		m.jobsPurged.WithLabelValues(rule).Add(float64(rows))
	}
}

// ModelAvailability is whether one model can take calls right now.
type ModelAvailability struct {
	Model     string
	Available bool
}

// SetModelAvailability sets where the availability gauge reads its values: fn
// is called at every scrape, so the gauge is never staler than the breaker.
// Calling it again replaces fn.
func (m *Metrics) SetModelAvailability(fn func() []ModelAvailability) { m.availability.set(fn) }

// availabilityCollector reports phraseforge_llm_model_available from a
// function at scrape time.
type availabilityCollector struct {
	desc *prometheus.Desc
	fn   atomic.Pointer[func() []ModelAvailability]
}

func newAvailabilityCollector() *availabilityCollector {
	return &availabilityCollector{desc: prometheus.NewDesc(
		"phraseforge_llm_model_available",
		"1 if the model can take calls, 0 while the circuit breaker holds it out.",
		[]string{"model"}, nil)}
}

func (c *availabilityCollector) set(fn func() []ModelAvailability) { c.fn.Store(&fn) }

func (c *availabilityCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *availabilityCollector) Collect(ch chan<- prometheus.Metric) {
	fn := c.fn.Load()
	if fn == nil {
		return
	}
	for _, m := range (*fn)() {
		value := 0.0
		if m.Available {
			value = 1
		}
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, value, m.Model)
	}
}

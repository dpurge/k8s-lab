// Package metrics holds the Prometheus metrics of knowledge's model calls and
// message bus requests, and serves them. Labels are configured ids only (provider,
// model, operation, outcome, direction), never a user, a prompt, a text or a
// vector.
package metrics

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// llmBuckets are the duration buckets, in seconds: from an embedding that takes
// a fraction of a second to a long reply from a model on a laptop CPU.
var llmBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1200, 1800}

// Metrics is the registry and the instruments.
type Metrics struct {
	registry *prometheus.Registry

	llmCalls        *prometheus.CounterVec
	llmTokens       *prometheus.CounterVec
	llmCost         *prometheus.CounterVec
	llmDuration     *prometheus.HistogramVec
	llmRecordErrors prometheus.Counter
	llmPurged       prometheus.Counter
	busRequests     *prometheus.CounterVec
	availability    *availabilityCollector
}

// New returns the metrics with the Go and process collectors.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		llmCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "knowledge_llm_calls_total",
			Help: "Attempts to call a model, by provider, model and outcome.",
		}, []string{"provider", "model", "outcome"}),
		llmTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "knowledge_llm_tokens_total",
			Help: "Tokens used by model calls, by provider, model and direction (input or output).",
		}, []string{"provider", "model", "direction"}),
		llmCost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "knowledge_llm_cost_usd_total",
			Help: "Cost of model calls in US dollars, by provider and model.",
		}, []string{"provider", "model"}),
		llmDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "knowledge_llm_call_duration_seconds",
			Help:    "Duration of model call attempts in seconds, by provider and model.",
			Buckets: llmBuckets,
		}, []string{"provider", "model"}),
		llmRecordErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "knowledge_llm_record_errors_total",
			Help: "Model calls that could not be recorded in the database.",
		}),
		llmPurged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "knowledge_llm_calls_purged_total",
			Help: "Model call records deleted by the retention purge.",
		}),
		busRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "knowledge_bus_requests_total",
			Help: "Requests handled over the message bus, by operation and outcome (ok or the error code).",
		}, []string{"operation", "outcome"}),
		availability: newAvailabilityCollector(),
	}
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.llmCalls, m.llmTokens, m.llmCost, m.llmDuration, m.llmRecordErrors, m.llmPurged, m.busRequests, m.availability,
	)
	return m
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveLLM counts one attempt. Tokens and cost are only added when positive,
// so a failed attempt that used none leaves those series alone.
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

// RecordError counts a call that could not be written to the database.
func (m *Metrics) RecordError() { m.llmRecordErrors.Inc() }

// ObserveBusRequest counts one request handled over the message bus; it has the
// signature of responder.Observer. Operation and outcome come from fixed sets.
func (m *Metrics) ObserveBusRequest(operation, outcome string) {
	m.busRequests.WithLabelValues(operation, outcome).Inc()
}

// RuleLLMCalls is the name of the retention rule of the llm_calls table.
const RuleLLMCalls = "llm_calls"

// Purged counts the rows a retention rule deleted; it has the signature of
// retention.WithOnPurged.
func (m *Metrics) Purged(rule string, rows int64) {
	if rule == RuleLLMCalls {
		m.llmPurged.Add(float64(rows))
	}
}

// ModelAvailability is one model's standing, for the availability gauge.
type ModelAvailability struct {
	Model     string
	Available bool
}

// SetModelAvailability sets where the gauge reads the models' standing from at
// every scrape.
func (m *Metrics) SetModelAvailability(fn func() []ModelAvailability) { m.availability.set(fn) }

// availabilityCollector reports knowledge_llm_model_available at scrape time:
// 1 for a model that can take calls, 0 while the breaker holds it out.
type availabilityCollector struct {
	desc *prometheus.Desc
	fn   atomic.Pointer[func() []ModelAvailability]
}

func newAvailabilityCollector() *availabilityCollector {
	return &availabilityCollector{desc: prometheus.NewDesc(
		"knowledge_llm_model_available",
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

// Package metrics holds the assistant's Prometheus collectors. They live in a
// registry of their own rather than the global default, so every test builds a
// fresh one and nothing registers twice.
package metrics

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the set of collectors, with their registry.
type Metrics struct {
	registry *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec

	tasks        *prometheus.CounterVec
	queueSeconds *prometheus.HistogramVec
	execSeconds  *prometheus.HistogramVec

	llmCalls        *prometheus.CounterVec
	llmTokens       *prometheus.CounterVec
	llmCost         *prometheus.CounterVec
	llmDuration     *prometheus.HistogramVec
	llmRecordErrors prometheus.Counter
	llmPurged       prometheus.Counter
	traceErrors     prometheus.Counter
	chatErrors      prometheus.Counter
	a2uiRejected    *prometheus.CounterVec
	busRequests     *prometheus.CounterVec
	busDuration     *prometheus.HistogramVec
	availability    *availabilityCollector
}

// taskBuckets suit work that waits seconds to minutes in a queue and runs for
// milliseconds to minutes.
var taskBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30, 60, 300, 900}

// New returns collectors registered in a new registry, together with the Go
// runtime and process collectors.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "assistant_http_requests_total",
			Help: "HTTP requests handled, by method, route template and status code.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "assistant_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds, by method and route template.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		tasks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "assistant_tasks_total",
			Help: "Tasks that finished, by source, workflow and final status.",
		}, []string{"source", "workflow", "status"}),
		queueSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "assistant_task_queue_seconds",
			Help:    "Time tasks waited in the queue before a worker started them, by workflow.",
			Buckets: taskBuckets,
		}, []string{"workflow"}),
		execSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "assistant_task_execution_seconds",
			Help:    "Time tasks spent executing, by workflow.",
			Buckets: taskBuckets,
		}, []string{"workflow"}),
	}
	m.llmCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "assistant_llm_calls_total",
		Help: "Attempts to call a model, by provider, model and outcome.",
	}, []string{"provider", "model", "outcome"})
	m.llmTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "assistant_llm_tokens_total",
		Help: "Tokens used by model calls, by user, provider, model and direction (input or output).",
	}, []string{"user", "provider", "model", "direction"})
	m.llmCost = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "assistant_llm_cost_usd_total",
		Help: "Cost of model calls in US dollars, by user, provider and model.",
	}, []string{"user", "provider", "model"})
	m.llmDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "assistant_llm_call_duration_seconds",
		Help:    "Duration of model call attempts in seconds, by provider and model.",
		Buckets: taskBuckets,
	}, []string{"provider", "model"})
	m.llmRecordErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "assistant_llm_record_errors_total",
		Help: "Model calls that could not be recorded in the database.",
	})
	m.llmPurged = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "assistant_llm_calls_purged_total",
		Help: "Model call records deleted by the retention purge.",
	})
	m.traceErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "assistant_trace_record_errors_total",
		Help: "Workflow trace rows that could not be written to the database.",
	})
	m.chatErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "assistant_chat_notify_errors_total",
		Help: "Assistant messages that could not be stored or delivered to a conversation.",
	})
	m.a2uiRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "assistant_a2ui_rejected_total",
		Help: "A2UI messages refused by validation, by source: interface (a workflow's) or event (the page's).",
	}, []string{"source"})
	for _, source := range []string{"interface", "event"} {
		m.a2uiRejected.WithLabelValues(source)
	}
	m.busRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "assistant_bus_requests_total",
		Help: "Requests the assistant sent to other apps over NATS, by app, operation and outcome: ok or the error code.",
	}, []string{"app", "operation", "outcome"})
	m.busDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "assistant_bus_request_duration_seconds",
		Help:    "Time from sending a request to another app to its answer, in seconds, by app and operation.",
		Buckets: prometheus.DefBuckets,
	}, []string{"app", "operation"})
	m.availability = newAvailabilityCollector()
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequests, m.httpDuration, m.tasks, m.queueSeconds, m.execSeconds,
		m.llmCalls, m.llmTokens, m.llmCost, m.llmDuration, m.llmRecordErrors, m.llmPurged, m.traceErrors, m.chatErrors, m.a2uiRejected, m.busRequests, m.busDuration, m.availability,
	)
	return m
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveHTTP records one handled request. route is the route template, not
// the URL, so that ids in paths do not create a series each.
func (m *Metrics) ObserveHTTP(method, route string, status int, elapsed time.Duration) {
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

// ObserveTask records a task that has finished.
func (m *Metrics) ObserveTask(source, workflow, status string, queued, executed time.Duration) {
	m.tasks.WithLabelValues(source, workflow, status).Inc()
	m.queueSeconds.WithLabelValues(workflow).Observe(queued.Seconds())
	m.execSeconds.WithLabelValues(workflow).Observe(executed.Seconds())
}

// ObserveLLM records one attempt to call a model. user is the id of the user
// the cost is attributed to. Tokens and cost are counted for every attempt; a
// failed attempt simply has none.
func (m *Metrics) ObserveLLM(user, provider, model, outcome string, inputTokens, outputTokens int, costUSD float64, elapsed time.Duration) {
	m.llmCalls.WithLabelValues(provider, model, outcome).Inc()
	m.llmDuration.WithLabelValues(provider, model).Observe(elapsed.Seconds())
	if inputTokens > 0 {
		m.llmTokens.WithLabelValues(user, provider, model, "input").Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		m.llmTokens.WithLabelValues(user, provider, model, "output").Add(float64(outputTokens))
	}
	if costUSD > 0 {
		m.llmCost.WithLabelValues(user, provider, model).Add(costUSD)
	}
}

// RecordError counts a model call that could not be written to the database.
func (m *Metrics) RecordError() { m.llmRecordErrors.Inc() }

// Purged counts the rows a retention rule deleted. The only rule is the
// llm_calls one, so the rule name is not a label.
func (m *Metrics) Purged(_ string, rows int64) { m.llmPurged.Add(float64(rows)) }

// TraceRecordError counts a workflow trace row that could not be written.
func (m *Metrics) TraceRecordError() { m.traceErrors.Inc() }

// ChatNotifyError counts an assistant message that could not be stored or
// delivered.
func (m *Metrics) ChatNotifyError() { m.chatErrors.Inc() }

// A2UIRejected counts an A2UI message that validation refused: source is
// "interface" for one a workflow showed, "event" for one the page sent.
func (m *Metrics) A2UIRejected(source string) { m.a2uiRejected.WithLabelValues(source).Inc() }

// ObserveBus records one request to another app. outcome is "ok" or the code of
// the error, so the label has a fixed, small set of values.
func (m *Metrics) ObserveBus(app, operation, outcome string, elapsed time.Duration) {
	m.busRequests.WithLabelValues(app, operation, outcome).Inc()
	m.busDuration.WithLabelValues(app, operation).Observe(elapsed.Seconds())
}

// ModelAvailability is whether one model can take calls right now.
type ModelAvailability struct {
	Model     string
	Available bool
}

// SetModelAvailability makes assistant_llm_model_available report, at every
// scrape, what fn returns: 1 for a model that is available, 0 for one the
// circuit breaker is holding out.
func (m *Metrics) SetModelAvailability(fn func() []ModelAvailability) {
	m.availability.set(fn)
}

type availabilityCollector struct {
	desc *prometheus.Desc
	fn   atomic.Pointer[func() []ModelAvailability]
}

func newAvailabilityCollector() *availabilityCollector {
	return &availabilityCollector{desc: prometheus.NewDesc(
		"assistant_llm_model_available",
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

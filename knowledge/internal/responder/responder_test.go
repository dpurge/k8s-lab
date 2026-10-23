package responder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"k8s-lab/shared/bus"
	"k8s-lab/shared/message"
	"k8s-lab/shared/ops"
	"knowledge/internal/ingest"
	"knowledge/internal/jobs"
	"knowledge/internal/qdrant"
)

// canary is a value that must never appear in a response: it sits in the fake
// job's source text, ref and tags.
const canary = "CANARY-SECRET-SOURCE-TEXT"

type fakeSearcher struct {
	got   qdrant.Search
	items []qdrant.ListItem
	err   error
}

func (f *fakeSearcher) Search(_ context.Context, s qdrant.Search) ([]qdrant.ListItem, error) {
	f.got = s
	return f.items, f.err
}

// fakeIngester applies the URL rule of the real service where it matters: it
// reports ingest.ErrValidation exactly as ingest.Service.StartURL does.
type fakeIngester struct {
	gotURL  string
	gotTags []string
	job     jobs.Job
	err     error
}

func (f *fakeIngester) StartURL(_ context.Context, rawURL string, tags []string) (jobs.Job, error) {
	f.gotURL, f.gotTags = rawURL, tags
	return f.job, f.err
}

type fakeJobs struct {
	byID map[string]jobs.Job
	err  error
}

func (f fakeJobs) Get(_ context.Context, id string) (jobs.Job, error) {
	if f.err != nil {
		return jobs.Job{}, f.err
	}
	if j, ok := f.byID[id]; ok {
		return j, nil
	}
	return jobs.Job{}, jobs.ErrNotFound
}

type observed struct {
	mu   sync.Mutex
	seen []string
}

func (o *observed) observe(operation, outcome string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, operation+"/"+outcome)
}

func newHandlers(svc Services, obs *observed) handlers {
	h := handlers{svc: svc, log: slog.New(slog.DiscardHandler)}
	if obs != nil {
		h.observe = obs.observe
	}
	return h
}

func request(t *testing.T, operation string, payload any) message.Envelope {
	t.Helper()
	env, err := message.NewRequest("assistant", App, operation, payload)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// call runs a handler and, on success, checks the response against the
// registry's response schema, as shared/bus does before it replies.
func call(t *testing.T, h bus.Handler, operation string, payload any) (json.RawMessage, error) {
	t.Helper()
	req := request(t, operation, payload)
	out, err := h(context.Background(), req)
	if err != nil {
		return nil, err
	}
	resp, err := message.Reply(req, App, out)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := newRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Validate(resp, reg); err != nil {
		t.Fatalf("the response breaks its schema: %v", err)
	}
	return resp.Payload, nil
}

func wantCode(t *testing.T, err error, code string) *message.Error {
	t.Helper()
	var coded *message.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error = %v, want a *message.Error with code %q", err, code)
	}
	return coded
}

func TestOnlyTheThreeOperationsAreRegistered(t *testing.T) {
	reg, err := newRegistry()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(reg.Names(), ",")
	if got != "documents_search,task_status,url_ingest" {
		t.Errorf("registered = %s, want documents_search, task_status and url_ingest", got)
	}
}

func TestDocumentsSearchMapsTheRequestAndBuildsSources(t *testing.T) {
	score := 0.82
	search := &fakeSearcher{items: []qdrant.ListItem{
		{ID: "d-1", Title: "Probes", Summary: "About probes", Tags: []string{"ops"}, Score: &score},
		{ID: "d-2", Title: "No score", Summary: "s"},
	}}
	h := newHandlers(Services{Search: search}, nil)
	payload, err := call(t, h.documentsSearch, ops.DocumentsSearch, ops.DocumentsSearchRequest{
		Query: "probe", Tags: []string{"ops", "k8s"}, Start: "2026-01-02T03:04:05Z", End: "2026-02-03T00:00:00Z", Limit: 3, Offset: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantStart, wantEnd := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)
	got := search.got
	if got.Query != "probe" || strings.Join(got.Tags, ",") != "ops,k8s" || got.Limit != 3 || got.Offset != 6 ||
		got.Start == nil || !got.Start.Equal(wantStart) || got.End == nil || !got.End.Equal(wantEnd) {
		t.Errorf("search = %+v, want query, tags, limit 3, offset 6 and both dates mapped", got)
	}
	resp, err := ops.ParseDocumentsSearchResponse(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Documents) != 2 {
		t.Fatalf("documents = %+v, want 2", resp.Documents)
	}
	d := resp.Documents[0]
	if d.ID != "d-1" || d.Title != "Probes" || d.Score != 0.82 || d.URL != "/?knowledge=d-1" || len(d.Tags) != 1 {
		t.Errorf("document 0 = %+v, want the item with url /?knowledge=d-1", d)
	}
	if d := resp.Documents[1]; d.Score != 0 || d.Tags == nil {
		t.Errorf("document 1 = %+v, want score 0 and an empty (not null) tag list", d)
	}
}

func TestDocumentsSearchDefaultsTheLimitAndLeavesDatesOpen(t *testing.T) {
	search := &fakeSearcher{}
	h := newHandlers(Services{Search: search}, nil)
	payload, err := call(t, h.documentsSearch, ops.DocumentsSearch, ops.DocumentsSearchRequest{Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if search.got.Limit != 5 || search.got.Start != nil || search.got.End != nil || search.got.Offset != 0 {
		t.Errorf("search = %+v, want limit 5 and no dates", search.got)
	}
	resp, err := ops.ParseDocumentsSearchResponse(payload)
	if err != nil || resp.Documents == nil || len(resp.Documents) != 0 {
		t.Errorf("response = %+v, %v; want an empty documents list", resp, err)
	}
}

func TestDocumentsSearchBackendFailureIsUnavailableWithAFixedText(t *testing.T) {
	search := &fakeSearcher{err: errors.New("qdrant POST /x: 500: " + canary)}
	h := newHandlers(Services{Search: search}, nil)
	_, err := call(t, h.documentsSearch, ops.DocumentsSearch, ops.DocumentsSearchRequest{Query: "x"})
	coded := wantCode(t, err, message.CodeUnavailable)
	if coded.Message != searchUnavailableText || strings.Contains(coded.Message, canary) {
		t.Errorf("message = %q, want the fixed text", coded.Message)
	}
}

func TestDocumentsSearchRejectsAnUnparsablePayload(t *testing.T) {
	h := newHandlers(Services{Search: &fakeSearcher{}}, nil)
	req := request(t, ops.DocumentsSearch, map[string]any{"query": ""})
	_, err := h.documentsSearch(context.Background(), req)
	wantCode(t, err, message.CodeInvalidPayload)
}

func TestURLIngestStartsTheJobAndReturnsItsIDAndStatus(t *testing.T) {
	ing := &fakeIngester{job: jobs.Job{ID: "job-1", Status: "running"}}
	h := newHandlers(Services{Ingest: ing}, nil)
	payload, err := call(t, h.urlIngest, ops.URLIngest, ops.URLIngestRequest{URL: "https://wiki.example/a", Tags: []string{" Ops ", "ops", "K8s"}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ops.ParseURLIngestResponse(payload)
	if err != nil || resp.TaskID != "job-1" || resp.Status != "running" {
		t.Errorf("response = %+v, %v; want job-1 and running", resp, err)
	}
	if ing.gotURL != "https://wiki.example/a" {
		t.Errorf("url = %q", ing.gotURL)
	}
	if want := qdrant.NormalizeTags([]string{" Ops ", "ops", "K8s"}); strings.Join(ing.gotTags, ",") != strings.Join(want, ",") {
		t.Errorf("tags = %v, want %v (as the HTTP handler normalizes them)", ing.gotTags, want)
	}
}

// TestURLIngestUsesTheRealServicesURLValidation: the handler reaches the real
// ingest.Service.StartURL, the call the HTTP ingest/url handler makes, which
// validates the URL before it touches any dependency (so a zero Service is
// enough). The HTTP handler maps its ErrValidation to 400; here it becomes
// invalid_payload with a fixed text.
func TestURLIngestUsesTheRealServicesURLValidation(t *testing.T) {
	real := &ingest.Service{}
	h := newHandlers(Services{Ingest: real}, nil)
	// Accepted by the payload schema's shape check, refused by StartURL.
	for _, bad := range []string{"http:///path", "https://%zz"} {
		if _, err := real.StartURL(context.Background(), bad, nil); !errors.Is(err, ingest.ErrValidation) {
			t.Fatalf("StartURL(%q) error = %v, want ErrValidation (the HTTP handler's rule)", bad, err)
		}
		_, err := h.urlIngest(context.Background(), request(t, ops.URLIngest, ops.URLIngestRequest{URL: bad}))
		coded := wantCode(t, err, message.CodeInvalidPayload)
		if coded.Message != invalidURLText {
			t.Errorf("url %q: message = %q, want the fixed text", bad, coded.Message)
		}
	}
	// Refused by the payload schema before the service: the same URLs the
	// service refuses for their scheme.
	for _, bad := range []string{"ftp://example.com/x", "file:///etc/passwd", "javascript:alert(1)"} {
		if _, err := real.StartURL(context.Background(), bad, nil); !errors.Is(err, ingest.ErrValidation) {
			t.Fatalf("StartURL(%q) error = %v, want ErrValidation", bad, err)
		}
		_, err := h.urlIngest(context.Background(), request(t, ops.URLIngest, map[string]any{"url": bad}))
		wantCode(t, err, message.CodeInvalidPayload)
	}
}

func TestURLIngestMapsServiceErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
		wantText string
	}{
		{"validation", fmt.Errorf("%w: %w", ingest.ErrValidation, errors.New(`url "https://x" has no host`)), message.CodeInvalidPayload, invalidURLText},
		{"another job running", ingest.ErrJobRunning, message.CodeUnavailable, ingestBusyText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHandlers(Services{Ingest: &fakeIngester{err: c.err}}, nil)
			_, err := h.urlIngest(context.Background(), request(t, ops.URLIngest, ops.URLIngestRequest{URL: "https://x.example/a"}))
			if coded := wantCode(t, err, c.wantCode); coded.Message != c.wantText {
				t.Errorf("message = %q, want %q", coded.Message, c.wantText)
			}
		})
	}
}

func TestURLIngestStartFailureIsInternalAndLeaksNoText(t *testing.T) {
	h := newHandlers(Services{Ingest: &fakeIngester{err: errors.New("insert job: " + canary)}}, nil)
	_, err := h.urlIngest(context.Background(), request(t, ops.URLIngest, ops.URLIngestRequest{URL: "https://x.example/a"}))
	var coded *message.Error
	if err == nil || errors.As(err, &coded) {
		t.Fatalf("error = %v, want a plain error that shared/bus turns into internal", err)
	}
	if got := outcomeOf(err); got != message.CodeInternal {
		t.Errorf("outcome = %q, want internal", got)
	}
}

func TestTaskStatusReturnsMetadataAndNeverTheSource(t *testing.T) {
	created := time.Date(2026, 10, 23, 9, 15, 0, 0, time.FixedZone("x", 3600))
	updated := created.Add(30 * time.Second)
	job := jobs.Job{
		ID: "11111111-1111-4111-8111-111111111111", Kind: "ingest", Status: "running", Step: "fetch",
		SourceKind: "url", SourceRef: "https://" + canary + ".example", SourceText: canary, SourceTags: []string{canary},
		CreatedAt: created, UpdatedAt: updated,
	}
	h := newHandlers(Services{Jobs: fakeJobs{byID: map[string]jobs.Job{job.ID: job}}}, nil)
	payload, err := call(t, h.taskStatus, ops.TaskStatus, ops.TaskStatusRequest{ID: job.ID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), canary) {
		t.Fatalf("the response carries source data: %s", payload)
	}
	resp, err := ops.ParseTaskStatusResponse(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := ops.TaskStatusResponse{ID: job.ID, Kind: "ingest", Status: "running", Step: "fetch", CreatedAt: "2026-10-23T08:15:00Z", UpdatedAt: "2026-10-23T08:15:30Z"}
	if resp != want {
		t.Errorf("response = %+v, want %+v", resp, want)
	}
}

func TestTaskStatusCarriesAndBoundsTheError(t *testing.T) {
	job := jobs.Job{ID: "j", Kind: "ingest", Status: "failed", Error: strings.Repeat("é", 1500), CreatedAt: time.Unix(0, 0), UpdatedAt: time.Unix(0, 0)}
	h := newHandlers(Services{Jobs: fakeJobs{byID: map[string]jobs.Job{"j": job}}}, nil)
	payload, err := call(t, h.taskStatus, ops.TaskStatus, ops.TaskStatusRequest{ID: "j"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ops.ParseTaskStatusResponse(payload)
	if err != nil || len([]rune(resp.Error)) != ops.MaxTaskErrorChars {
		t.Errorf("error length = %d, %v; want %d characters", len([]rune(resp.Error)), err, ops.MaxTaskErrorChars)
	}
}

func TestTaskStatusUnknownIDIsNotFoundAndOtherFailuresAreInternal(t *testing.T) {
	h := newHandlers(Services{Jobs: fakeJobs{}}, nil)
	_, err := h.taskStatus(context.Background(), request(t, ops.TaskStatus, ops.TaskStatusRequest{ID: "nope"}))
	if coded := wantCode(t, err, message.CodeNotFound); coded.Message != jobNotFoundText {
		t.Errorf("message = %q, want the fixed text", coded.Message)
	}
	h = newHandlers(Services{Jobs: fakeJobs{err: errors.New("db down " + canary)}}, nil)
	_, err = h.taskStatus(context.Background(), request(t, ops.TaskStatus, ops.TaskStatusRequest{ID: "x"}))
	if got := outcomeOf(err); got != message.CodeInternal {
		t.Errorf("outcome = %q, want internal", got)
	}
}

func TestInstrumentLogsOnlyOperationIDAndOutcomeAndCounts(t *testing.T) {
	var logged strings.Builder
	obs := &observed{}
	h := handlers{
		svc:     Services{Jobs: fakeJobs{}},
		observe: obs.observe,
		log:     slog.New(slog.NewTextHandler(&logged, nil)),
	}
	wrapped := h.instrument(ops.TaskStatus, h.taskStatus)
	req := request(t, ops.TaskStatus, ops.TaskStatusRequest{ID: "secret-looking-id-" + canary})
	if _, err := wrapped(context.Background(), req); err == nil {
		t.Fatal("want not_found")
	}
	line := logged.String()
	for _, want := range []string{"operation=task_status", "id=" + req.ID, "outcome=not_found"} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q lacks %q", line, want)
		}
	}
	if strings.Contains(line, canary) {
		t.Errorf("the log carries a payload value: %s", line)
	}
	if len(obs.seen) != 1 || obs.seen[0] != "task_status/not_found" {
		t.Errorf("observed = %v, want task_status/not_found", obs.seen)
	}
}

func TestRunDoesNotConnectWhenTheURLIsEmpty(t *testing.T) {
	old := connect
	defer func() { connect = old }()
	connect = func(bus.Config) (*nats.Conn, error) {
		t.Error("connect was called with an empty NATS URL")
		return nil, errors.New("must not connect")
	}
	for _, url := range []string{"", "   "} {
		stop := Run(url, App, Services{}, nil, slog.New(slog.DiscardHandler))
		if stop == nil {
			t.Fatal("stop is nil, want a no-op")
		}
		stop()
	}
}

func TestRunSurvivesAFailedConnectionWithAWarning(t *testing.T) {
	old := connect
	defer func() { connect = old }()
	connect = func(bus.Config) (*nats.Conn, error) { return nil, errors.New("no server") }
	var logged strings.Builder
	stop := Run("nats://nowhere:4222", App, Services{}, nil, slog.New(slog.NewTextHandler(&logged, nil)))
	stop()
	if !strings.Contains(logged.String(), "level=WARN") || !strings.Contains(logged.String(), "knowledge serves HTTP without it") {
		t.Errorf("log = %q, want the warning", logged.String())
	}
}

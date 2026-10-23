package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"k8s-lab/shared/message"
	"k8s-lab/shared/ops"

	"phraseforge/internal/jobs"
)

const (
	knownID = "0b9f7c2e-1d3a-4c55-9a10-6f2f6a1d8e01"
	// The canaries stand for what a task_status answer must never carry.
	canaryPayload = "CANARY-PAYLOAD-user-text"
	canaryResult  = "CANARY-RESULT-user-text"
	canaryUser    = int64(4242424)
)

type fakeStore struct {
	job   jobs.Job
	err   error
	asked []string
}

func (f *fakeStore) Get(_ context.Context, id string) (jobs.Job, error) {
	f.asked = append(f.asked, id)
	if f.err != nil {
		return jobs.Job{}, f.err
	}
	if id != f.job.ID {
		return jobs.Job{}, pgx.ErrNoRows
	}
	return f.job, nil
}

type observation struct{ operation, outcome string }

type fakeObserver struct {
	mu   sync.Mutex
	seen []observation
}

func (f *fakeObserver) ObserveBusRequest(operation, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, observation{operation, outcome})
}

func canaryJob() jobs.Job {
	user := canaryUser
	return jobs.Job{
		ID:        knownID,
		Kind:      "process_text",
		Priority:  jobs.PriorityBackground,
		Status:    jobs.StatusRunning,
		Payload:   json.RawMessage(`{"content":"` + canaryPayload + `","user_id":4242424}`),
		Result:    json.RawMessage(`{"text":"` + canaryResult + `"}`),
		Step:      "clean",
		Progress:  "2/5",
		Error:     "model timed out",
		CreatedAt: time.Date(2026, 10, 23, 9, 15, 0, 0, time.FixedZone("CEST", 2*3600)),
		UpdatedAt: time.Date(2026, 10, 23, 9, 15, 30, 500, time.UTC),
		UserID:    &user,
	}
}

type fixture struct {
	h        *handlers
	store    *fakeStore
	observer *fakeObserver
	logs     *bytes.Buffer
}

func newFixture(job jobs.Job) *fixture {
	logs := &bytes.Buffer{}
	store := &fakeStore{job: job}
	observer := &fakeObserver{}
	return &fixture{
		h:        &handlers{store: store, observer: observer, log: slog.New(slog.NewTextHandler(logs, nil))},
		store:    store,
		observer: observer,
		logs:     logs,
	}
}

func statusRequest(t *testing.T, id string) message.Envelope {
	t.Helper()
	req, err := message.NewRequest("assistant", App, ops.TaskStatus, ops.TaskStatusRequest{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var merr *message.Error
	if !errors.As(err, &merr) || merr.Code != code {
		t.Fatalf("error = %v, want a *message.Error with code %q", err, code)
	}
}

func TestTaskStatusMapsTheJobToTheDocumentedFields(t *testing.T) {
	f := newFixture(canaryJob())
	payload, err := f.h.taskStatus(context.Background(), statusRequest(t, knownID))
	if err != nil {
		t.Fatal(err)
	}
	want := ops.TaskStatusResponse{
		ID: knownID, Kind: "process_text", Status: "running", Step: "clean", Progress: "2/5", Error: "model timed out",
		CreatedAt: "2026-10-23T07:15:00Z", UpdatedAt: "2026-10-23T09:15:30.0000005Z",
	}
	if !reflect.DeepEqual(payload, want) {
		t.Errorf("payload = %+v, want %+v", payload, want)
	}
	reg, err := newRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateResponsePayload(ops.TaskStatus, encoded); err != nil {
		t.Errorf("the response breaks its schema: %v\n%s", err, encoded)
	}
	if !reflect.DeepEqual(f.observer.seen, []observation{{ops.TaskStatus, "ok"}}) {
		t.Errorf("observed = %v", f.observer.seen)
	}
}

func TestTaskStatusNeverCarriesThePayloadTheResultOrTheUser(t *testing.T) {
	f := newFixture(canaryJob())
	payload, err := f.h.taskStatus(context.Background(), statusRequest(t, knownID))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := message.Reply(statusRequest(t, knownID), App, payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := message.Encode(reply)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{canaryPayload, canaryResult, "4242424", `"payload":{"content"`, "user_id"} {
		if strings.Contains(string(encoded), canary) {
			t.Errorf("the encoded response contains %q: %s", canary, encoded)
		}
		if strings.Contains(f.logs.String(), canary) {
			t.Errorf("the log contains %q: %s", canary, f.logs.String())
		}
	}
	// The payload keys are exactly the documented ones, none for a payload or a result.
	var keys map[string]any
	if err := json.Unmarshal(reply.Payload, &keys); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range keys {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"created_at", "error", "id", "kind", "progress", "status", "step", "updated_at"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload keys = %v, want %v", got, want)
	}
}

func TestTaskStatusLeavesOutWhatTheJobDoesNotHave(t *testing.T) {
	job := canaryJob()
	job.Step, job.Progress, job.Error = "", "", ""
	payload, err := newFixture(job).h.taskStatus(context.Background(), statusRequest(t, knownID))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(payload)
	for _, key := range []string{`"step"`, `"progress"`, `"error"`} {
		if strings.Contains(string(encoded), key) {
			t.Errorf("%s present for an empty value: %s", key, encoded)
		}
	}
}

func TestTaskStatusCutsALongErrorOnACharacterBoundary(t *testing.T) {
	job := canaryJob()
	job.Error = strings.Repeat("ż", ops.MaxTaskErrorChars+50)
	payload, err := newFixture(job).h.taskStatus(context.Background(), statusRequest(t, knownID))
	if err != nil {
		t.Fatal(err)
	}
	got := payload.(ops.TaskStatusResponse).Error
	if got != strings.Repeat("ż", ops.MaxTaskErrorChars) {
		t.Errorf("error has %d characters, want %d whole ones", len([]rune(got)), ops.MaxTaskErrorChars)
	}
	reg, _ := newRegistry()
	encoded, _ := json.Marshal(payload)
	if err := reg.ValidateResponsePayload(ops.TaskStatus, encoded); err != nil {
		t.Errorf("the cut response breaks its schema: %v", err)
	}
}

func TestTaskStatusOfAnUnknownIDIsNotFound(t *testing.T) {
	f := newFixture(canaryJob())
	_, err := f.h.taskStatus(context.Background(), statusRequest(t, "11111111-2222-4333-8444-555555555555"))
	wantCode(t, err, message.CodeNotFound)
	if !reflect.DeepEqual(f.observer.seen, []observation{{ops.TaskStatus, "not_found"}}) {
		t.Errorf("observed = %v", f.observer.seen)
	}
}

func TestTaskStatusOfTextThatIsNoJobIDIsNotFoundWithoutAskingTheStore(t *testing.T) {
	f := newFixture(canaryJob())
	for _, id := range []string{"t-1001", "1", "../etc/passwd", knownID + "0", "0b9f7c2e-1d3a-4c55-9a10-6f2f6a1d8e0g"} {
		_, err := f.h.taskStatus(context.Background(), statusRequest(t, id))
		wantCode(t, err, message.CodeNotFound)
	}
	if len(f.store.asked) != 0 {
		t.Errorf("the store was asked for %v: a uuid column would fail on text that is no uuid", f.store.asked)
	}
}

func TestTaskStatusAnUppercaseUUIDIsLookedUp(t *testing.T) {
	f := newFixture(canaryJob())
	id := strings.ToUpper(knownID)
	_, _ = f.h.taskStatus(context.Background(), statusRequest(t, id))
	if !reflect.DeepEqual(f.store.asked, []string{id}) {
		t.Errorf("asked = %v, want the id passed to the store as given", f.store.asked)
	}
}

func TestTaskStatusStoreFailureIsAnInternalErrorThatKeepsItsTextInside(t *testing.T) {
	f := newFixture(canaryJob())
	f.store.err = errors.New("dial tcp 10.1.2.3:5432: connection refused")
	_, err := f.h.taskStatus(context.Background(), statusRequest(t, knownID))
	var merr *message.Error
	if err == nil || errors.As(err, &merr) {
		t.Fatalf("error = %v: a store failure must not be a *message.Error, so the responder answers its fixed internal text", err)
	}
	if !reflect.DeepEqual(f.observer.seen, []observation{{ops.TaskStatus, "internal"}}) {
		t.Errorf("observed = %v", f.observer.seen)
	}
	if strings.Contains(f.logs.String(), "10.1.2.3") {
		t.Errorf("the handler logged the database text: %s", f.logs.String())
	}
}

func TestTaskStatusRefusesAPayloadThatIsNotARequest(t *testing.T) {
	f := newFixture(canaryJob())
	for name, raw := range map[string]string{
		"no id":         `{}`,
		"extra field":   `{"id":"` + knownID + `","payload":true}`,
		"id is a count": `{"id":7}`,
	} {
		req := statusRequest(t, knownID)
		req.Payload = json.RawMessage(raw)
		_, err := f.h.taskStatus(context.Background(), req)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		wantCode(t, err, message.CodeInvalidPayload)
	}
	if len(f.store.asked) != 0 {
		t.Errorf("the store was asked for %v", f.store.asked)
	}
}

func TestHandlersLogTheOperationTheIDAndTheOutcomeOnly(t *testing.T) {
	f := newFixture(canaryJob())
	if _, err := f.h.taskStatus(context.Background(), statusRequest(t, knownID)); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(f.logs.String())
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("want one log line, got:\n%s", line)
	}
	for _, want := range []string{"operation=task_status", "id=" + knownID, "outcome=ok"} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q lacks %q", line, want)
		}
	}
	for _, leak := range []string{"process_text", "model timed out", "clean", "2/5"} {
		if strings.Contains(line, leak) {
			t.Errorf("log %q names %q: only the operation, the id and the outcome belong there", line, leak)
		}
	}
}

func TestHandlersWorkWithoutAnObserver(t *testing.T) {
	f := newFixture(canaryJob())
	f.h.observer = nil
	if _, err := f.h.taskStatus(context.Background(), statusRequest(t, knownID)); err != nil {
		t.Fatal(err)
	}
}

func TestTheRegistryHoldsOnlyTheOperationsAnswered(t *testing.T) {
	reg, err := newRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Names(); !reflect.DeepEqual(got, []string{ops.TaskStatus}) {
		t.Errorf("registry = %v, want only task_status", got)
	}
	for _, name := range []string{ops.URLIngest, ops.DocumentsSearch} {
		if reg.Has(name) {
			t.Errorf("the registry has %s: phraseforge does not answer it, so the responder must say unknown_operation", name)
		}
	}
}

func TestStartIsOffWhenNoURLIsConfigured(t *testing.T) {
	for _, url := range []string{"", "   "} {
		service, err := Start(url, App, &fakeStore{}, nil, nil)
		if service != nil || err != nil {
			t.Errorf("Start(%q) = %v, %v; want nil, nil: the app never connects", url, service, err)
		}
		service.Stop() // a nil Service is a stopped one
	}
}

func TestStartReportsAServerThatCannotBeReachedAndLeavesNothingRunning(t *testing.T) {
	service, err := Start("nats://127.0.0.1:1", App, &fakeStore{}, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err == nil || service != nil {
		t.Fatalf("Start = %v, %v; want an error and no service", service, err)
	}
}

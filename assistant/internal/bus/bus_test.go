package bus

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"assistant/internal/callctx"
	"k8s-lab/shared/message"
	"k8s-lab/shared/ops"
)

// fakeRequester answers every request with a scripted payload or error and
// remembers the request it was given, as the envelope the real client builds.
type fakeRequester struct {
	payload any
	err     error

	calls   int
	request message.Envelope
}

func (f *fakeRequester) Request(_ context.Context, to, operation string, payload any, opts ...message.Option) (message.Envelope, error) {
	f.calls++
	req, err := message.NewRequest(appName, to, operation, payload, opts...)
	if err != nil {
		return message.Envelope{}, err
	}
	f.request = req
	if f.err != nil {
		return message.Envelope{}, f.err
	}
	return message.Reply(req, to, f.payload)
}

func TestSearchDocumentsAsksKnowledgeAndParsesTheAnswer(t *testing.T) {
	want := ops.DocumentsSearchResponse{Documents: []ops.Document{{ID: "d-1", Title: "Probes", Summary: "s", Tags: []string{"ops"}, Score: 0.5, URL: "/documents/d-1"}}, Total: 1}
	fake := &fakeRequester{payload: want}
	got, err := newClient(fake).SearchDocuments(context.Background(), ops.DocumentsSearchRequest{Query: "probes", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if fake.request.To != "knowledge" || fake.request.Operation != ops.DocumentsSearch {
		t.Errorf("sent to %q operation %q", fake.request.To, fake.request.Operation)
	}
}

func TestIngestURLAsksTheNamedApp(t *testing.T) {
	for _, app := range []string{Knowledge, Phraseforge} {
		fake := &fakeRequester{payload: ops.URLIngestResponse{TaskID: "t-1", Status: "queued"}}
		got, err := newClient(fake).IngestURL(context.Background(), app, ops.URLIngestRequest{URL: "https://wiki.example/a"})
		if err != nil || got.TaskID != "t-1" || got.Status != "queued" {
			t.Errorf("%s: got %+v, %v", app, got, err)
		}
		if fake.request.To != app || fake.request.Operation != ops.URLIngest {
			t.Errorf("%s: sent to %q operation %q", app, fake.request.To, fake.request.Operation)
		}
	}
}

func TestTaskStatusAsksTheNamedAppForTheID(t *testing.T) {
	want := ops.TaskStatusResponse{ID: "t-1", Kind: "url_ingest", Status: "running", Step: "fetch", CreatedAt: "2026-10-23T09:15:00Z", UpdatedAt: "2026-10-23T09:15:30Z"}
	for _, app := range []string{Knowledge, Phraseforge} {
		fake := &fakeRequester{payload: want}
		got, err := newClient(fake).TaskStatus(context.Background(), app, "t-1")
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v", app, got, err)
		}
		if fake.request.To != app || fake.request.Operation != ops.TaskStatus {
			t.Errorf("%s: sent to %q operation %q", app, fake.request.To, fake.request.Operation)
		}
		sent, err := ops.ParseTaskStatusRequest(fake.request.Payload)
		if err != nil || sent.ID != "t-1" {
			t.Errorf("%s: request payload %s, %v", app, fake.request.Payload, err)
		}
	}
}

func TestTheContextsTaskAndUserGoIntoTheEnvelope(t *testing.T) {
	fake := &fakeRequester{payload: ops.URLIngestResponse{TaskID: "t-1", Status: "queued"}}
	ctx := callctx.With(context.Background(), callctx.Info{TaskID: "task-9", UserID: "user-3"})
	if _, err := newClient(fake).IngestURL(ctx, Knowledge, ops.URLIngestRequest{URL: "https://wiki.example/a"}); err != nil {
		t.Fatal(err)
	}
	want := &message.Context{UserID: "user-3", TaskID: "task-9"}
	if !reflect.DeepEqual(fake.request.Context, want) {
		t.Errorf("context = %+v, want %+v", fake.request.Context, want)
	}
}

func TestACallWithoutAUserSendsNoContext(t *testing.T) {
	fake := &fakeRequester{payload: ops.URLIngestResponse{TaskID: "t-1", Status: "queued"}}
	if _, err := newClient(fake).IngestURL(context.Background(), Knowledge, ops.URLIngestRequest{URL: "https://wiki.example/a"}); err != nil {
		t.Fatal(err)
	}
	if fake.request.Context != nil {
		t.Errorf("context = %+v, want none", fake.request.Context)
	}
}

func TestAnAppThatAnswersNoneOfTheOperationsIsRefusedBeforeSending(t *testing.T) {
	fake := &fakeRequester{}
	c := newClient(fake)
	for _, app := range []string{"", "dictionary", "Knowledge", "assistant"} {
		_, err := c.IngestURL(context.Background(), app, ops.URLIngestRequest{URL: "https://wiki.example/a"})
		wantCode(t, "IngestURL "+app, err, message.CodeInvalidMessage)
		_, err = c.TaskStatus(context.Background(), app, "t-1")
		wantCode(t, "TaskStatus "+app, err, message.CodeInvalidMessage)
	}
	if fake.calls != 0 {
		t.Errorf("%d requests were sent, want none", fake.calls)
	}
}

func TestAnErrorFromTheOtherAppComesBackWithItsCode(t *testing.T) {
	fake := &fakeRequester{err: &message.Error{Code: message.CodeNotFound, Message: "no such task"}}
	_, err := newClient(fake).TaskStatus(context.Background(), Knowledge, "t-404")
	wantCode(t, "TaskStatus", err, message.CodeNotFound)
	var coded *message.Error
	if !errors.As(err, &coded) || coded.Message != "no such task" {
		t.Errorf("err = %v, want the message kept", err)
	}
}

func TestAResponsePayloadThatBreaksTheSchemaIsInvalidMessage(t *testing.T) {
	// The second payload has a field the schema does not allow.
	fake := &fakeRequester{payload: map[string]any{"task_id": "t-1", "status": "queued", "extra": true}}
	_, err := newClient(fake).IngestURL(context.Background(), Knowledge, ops.URLIngestRequest{URL: "https://wiki.example/a"})
	wantCode(t, "IngestURL", err, message.CodeInvalidMessage)

	fake = &fakeRequester{payload: map[string]any{"documents": "none"}}
	_, err = newClient(fake).SearchDocuments(context.Background(), ops.DocumentsSearchRequest{Query: "q"})
	wantCode(t, "SearchDocuments", err, message.CodeInvalidMessage)
}

func TestRetryableIsTrueForUnavailableAndTimeoutOnly(t *testing.T) {
	cases := map[string]bool{
		message.CodeUnavailable: true, message.CodeTimeout: true,
		message.CodeInvalidMessage: false, message.CodeInvalidPayload: false, message.CodeNotFound: false, message.CodeInternal: false,
	}
	for code, want := range cases {
		fake := &fakeRequester{err: &message.Error{Code: code, Message: "m"}}
		_, err := newClient(fake).SearchDocuments(context.Background(), ops.DocumentsSearchRequest{Query: "q"})
		if got := Retryable(err); got != want {
			t.Errorf("%s: Retryable = %v, want %v", code, got, want)
		}
	}
	if Retryable(errors.New("plain")) {
		t.Error("a plain error is not retryable")
	}
}

type recordedBus struct{ app, operation, outcome string }

type recordingObserver struct{ seen []recordedBus }

func (r *recordingObserver) ObserveBus(app, operation, outcome string, _ time.Duration) {
	r.seen = append(r.seen, recordedBus{app, operation, outcome})
}

func TestTheObserverSeesOkAndTheErrorCode(t *testing.T) {
	obs := &recordingObserver{}
	fake := &fakeRequester{payload: ops.URLIngestResponse{TaskID: "t-1", Status: "queued"}}
	c := newClient(fake, WithObserver(obs))
	if _, err := c.IngestURL(context.Background(), Phraseforge, ops.URLIngestRequest{URL: "https://wiki.example/a"}); err != nil {
		t.Fatal(err)
	}
	fake.err = &message.Error{Code: message.CodeTimeout, Message: "m"}
	_, _ = c.SearchDocuments(context.Background(), ops.DocumentsSearchRequest{Query: "q"})
	want := []recordedBus{
		{"phraseforge", ops.URLIngest, "ok"},
		{"knowledge", ops.DocumentsSearch, message.CodeTimeout},
	}
	if !reflect.DeepEqual(obs.seen, want) {
		t.Errorf("observed %v, want %v", obs.seen, want)
	}
}

func TestWithDestinationsRenamesOnlyTheTwoApps(t *testing.T) {
	fake := &fakeRequester{payload: ops.URLIngestResponse{TaskID: "t-1", Status: "queued"}}
	c := newClient(fake, WithDestinations(map[string]string{Knowledge: "k-test", "other": "x"}))
	if _, err := c.IngestURL(context.Background(), Knowledge, ops.URLIngestRequest{URL: "https://wiki.example/a"}); err != nil {
		t.Fatal(err)
	}
	if fake.request.To != "k-test" {
		t.Errorf("sent to %q, want k-test", fake.request.To)
	}
	if _, err := c.IngestURL(context.Background(), "other", ops.URLIngestRequest{URL: "https://wiki.example/a"}); err == nil {
		t.Error("an app that is not knowledge or phraseforge must stay refused")
	}
}

func TestCloseOfANilClientIsFine(t *testing.T) {
	var c *Client
	if err := c.Close(); err != nil {
		t.Error(err)
	}
}

func wantCode(t *testing.T, what string, err error, code string) {
	t.Helper()
	var coded *message.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Errorf("%s: err = %v, want code %s", what, err, code)
	}
}

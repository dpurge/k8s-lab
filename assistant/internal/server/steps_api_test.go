package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"assistant/internal/metrics"
	"assistant/internal/tasks"
	"assistant/internal/tasksteps"
)

type answerTasks struct {
	failingTasks
	err      error
	answered []string
}

func (a *answerTasks) Resume(_ context.Context, id string, message json.RawMessage) (tasks.Task, error) {
	if a.err != nil {
		return tasks.Task{}, a.err
	}
	a.answered = append(a.answered, id+"="+string(message))
	return tasks.Task{ID: id, UserID: someID, WorkflowID: "learn-topic", Source: tasks.SourceAPI, Status: tasks.StatusPending, Payload: json.RawMessage(`{}`)}, nil
}

type stepsStore struct {
	steps []tasksteps.Step
	err   error
}

func (s stepsStore) ForTask(context.Context, string) ([]tasksteps.Step, error) { return s.steps, s.err }

func stepsRouter(t *testing.T, ts TaskStore, st StepStore) (http.Handler, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	return NewRouter(Deps{DB: fakePinger{}, Tasks: ts, Steps: st, Calls: failingCalls{}, Metrics: metrics.New(), Log: slog.New(slog.NewJSONHandler(logs, nil))}), logs
}

func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

const someID = "11111111-1111-1111-1111-111111111111"

func TestAnswerMapsEachOutcomeToItsStatusAndMessage(t *testing.T) {
	s := loadSpec(t)
	cases := []struct {
		name   string
		err    error
		body   string
		status int
		code   string
		text   string
	}{
		{"ok", nil, `{"message":"Ada"}`, 200, "", ""},
		{"a structured answer", nil, `{"message":{"choice":2}}`, 200, "", ""},
		{"false is an answer", nil, `{"message":false}`, 200, "", ""},
		{"zero is an answer", nil, `{"message":0}`, 200, "", ""},
		{"unknown task", tasks.ErrNotFound, `{"message":"a"}`, 404, "not_found", "task not found"},
		{"not waiting", tasks.ErrNotWaiting, `{"message":"a"}`, 409, "conflict", "not waiting for an answer"},
		{"missing", nil, `{}`, 400, "invalid_request", "message is required"},
		{"null", nil, `{"message":null}`, 400, "invalid_request", "message is required"},
		{"empty body", nil, ``, 400, "invalid_request", "empty"},
		{"unknown field", nil, `{"message":"a","x":1}`, 400, "invalid_request", "unknown field"},
	}
	for _, c := range cases {
		store := &answerTasks{err: c.err}
		h, _ := stepsRouter(t, store, stepsStore{})
		rec := request(h, "POST", "/api/v1/tasks/"+someID+"/messages", c.body)
		s.check("POST", "/api/v1/tasks/{id}/messages", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.code) || !strings.Contains(rec.Body.String(), c.text) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		if c.status != 200 && len(store.answered) != 0 {
			t.Errorf("%s: a refused answer must not reach the store", c.name)
		}
	}
}

func TestTheAnswerReachesTheStoreExactlyAsSent(t *testing.T) {
	store := &answerTasks{}
	h, _ := stepsRouter(t, store, stepsStore{})
	request(h, "POST", "/api/v1/tasks/"+someID+"/messages", `{"message":{"a":[1,2],"b":"عربي"}}`)
	if len(store.answered) != 1 || store.answered[0] != someID+`={"a":[1,2],"b":"عربي"}` {
		t.Errorf("answered = %v", store.answered)
	}
}

func TestStepsOfAnUnknownTaskAre404AndAStoreFailureIs500WithTheCauseLogged(t *testing.T) {
	h, _ := stepsRouter(t, notFoundTasks{}, stepsStore{})
	if rec := request(h, "GET", "/api/v1/tasks/"+someID+"/steps", ""); rec.Code != 404 {
		t.Errorf("unknown: %d", rec.Code)
	}
	h, logs := stepsRouter(t, okTasks{}, stepsStore{err: errSecret})
	rec := request(h, "GET", "/api/v1/tasks/"+someID+"/steps", "")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), errSecret.Error()) || !strings.Contains(logs.String(), errSecret.Error()) {
		t.Errorf("%d %s | %s", rec.Code, rec.Body, logs)
	}
}

type notFoundTasks struct{ failingTasks }

func (notFoundTasks) Get(context.Context, string) (tasks.Task, error) {
	return tasks.Task{}, tasks.ErrNotFound
}

func TestStepsAreReturnedAsTheyWereStored(t *testing.T) {
	reply, errText := "raw reply", "the reply is not valid JSON"
	h, _ := stepsRouter(t, okTasks{}, stepsStore{steps: []tasksteps.Step{
		{ID: 1, TaskID: someID, Step: 1, Attempt: 1, State: "intake", Outcome: "retry", Error: &errText, Reply: &reply},
	}})
	s := loadSpec(t)
	rec := request(h, "GET", "/api/v1/tasks/"+someID+"/steps", "")
	s.check("GET", "/api/v1/tasks/{id}/steps", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
	got := decode[stepList](t, rec)
	if len(got.Items) != 1 || *got.Items[0].Error != errText || *got.Items[0].Reply != "raw reply" || string(got.Items[0].Action) != "null" {
		t.Errorf("items = %+v", got.Items)
	}
	if !strings.Contains(rec.Body.String(), `"action":null`) || !strings.Contains(rec.Body.String(), `"tool":null`) {
		t.Errorf("absent values are null: %s", rec.Body)
	}
}

package message

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

const (
	testRequestID  = "3f0c1c52-8a43-4c1e-9d0e-6a1b2f7d9e10"
	testResponseID = "b7a1e0d4-5c2f-4f6a-8b13-0d9e4c7a2f55"
)

// validRequest is a request that passes every rule; tests change one thing.
func validRequest() Envelope {
	return Envelope{
		SchemaVersion: 1, ID: testRequestID, Type: TypeRequest, CorrelationID: testRequestID,
		From: "assistant", To: "knowledge", CreatedAt: "2026-10-23T09:15:00Z", Operation: "documents_search",
		Payload: json.RawMessage(`{"query":"probes"}`),
	}
}

func validResponse() Envelope {
	e := validRequest()
	e.ID, e.Type, e.From, e.To = testResponseID, TypeResponse, "knowledge", "assistant"
	return e
}

func validEvent() Envelope {
	e := validResponse()
	e.Type = TypeEvent
	return e
}

func validError() Envelope {
	e := validResponse()
	e.Type, e.Payload = TypeError, nil
	e.Error = &ErrorBody{Code: CodeNotFound, Message: "No document has that id.", Retryable: false}
	return e
}

// codeOf returns the code of a *Error, failing the test for anything else.
func codeOf(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want *Error, got %T %v", err, err)
	}
	return e
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fixClock makes constructors deterministic for the test.
func fixClock(t *testing.T, ids ...string) {
	t.Helper()
	oldNow, oldID := now, newID
	t.Cleanup(func() { now, newID = oldNow, oldID })
	now = func() time.Time { return time.Date(2026, 10, 23, 11, 15, 0, 0, time.FixedZone("CEST", 2*3600)) }
	next := 0
	newID = func() string {
		id := ids[next%len(ids)]
		next++
		return id
	}
}

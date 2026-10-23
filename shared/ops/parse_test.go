package ops

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"k8s-lab/shared/message"
)

func TestParseReturnsTheTypedValues(t *testing.T) {
	search, err := ParseDocumentsSearchRequest(mustJSON(t, with(baseSearchRequest, object{"tags": []any{"ops"}, "start": "2026-01-01T00:00:00Z", "limit": 7, "offset": 3})))
	if err != nil {
		t.Fatal(err)
	}
	if want := (DocumentsSearchRequest{Query: "kubernetes liveness probe", Tags: []string{"ops"}, Start: "2026-01-01T00:00:00Z", Limit: 7, Offset: 3}); !reflect.DeepEqual(search, want) {
		t.Errorf("search request: %+v", search)
	}
	found, err := ParseDocumentsSearchResponse(mustJSON(t, with(baseSearchResponse, object{"total": 9})))
	if err != nil {
		t.Fatal(err)
	}
	if want := (DocumentsSearchResponse{Documents: []Document{{ID: "d-17", Title: "Probes", Summary: "Liveness and readiness probes.", Tags: []string{"ops"}, Score: 0.82, URL: "/documents/d-17"}}, Total: 9}); !reflect.DeepEqual(found, want) {
		t.Errorf("search response: %+v", found)
	}
	ingest, err := ParseURLIngestRequest(mustJSON(t, baseIngestRequest))
	if err != nil || ingest.URL != "https://wiki.example/probes" {
		t.Errorf("ingest request: %+v %v", ingest, err)
	}
	accepted, err := ParseURLIngestResponse(mustJSON(t, baseIngestResponse))
	if err != nil || accepted != (URLIngestResponse{TaskID: "t-1001", Status: "queued"}) {
		t.Errorf("ingest response: %+v %v", accepted, err)
	}
	status, err := ParseTaskStatusRequest(mustJSON(t, baseStatusRequest))
	if err != nil || status.ID != "t-1001" {
		t.Errorf("status request: %+v %v", status, err)
	}
	state, err := ParseTaskStatusResponse(mustJSON(t, with(baseStatusResponse, object{"progress": "1/2"})))
	if err != nil || state.Progress != "1/2" || state.UpdatedAt != "2026-10-23T09:15:30.5Z" {
		t.Errorf("status response: %+v %v", state, err)
	}
}

func TestParseRejectsAnUnknownField(t *testing.T) {
	payloads := map[string]func(json.RawMessage) error{
		`{"query":"x","colour":"red"}`:                     func(p json.RawMessage) error { _, err := ParseDocumentsSearchRequest(p); return err },
		`{"documents":[],"colour":"red"}`:                  func(p json.RawMessage) error { _, err := ParseDocumentsSearchResponse(p); return err },
		`{"url":"https://a.org","colour":"red"}`:           func(p json.RawMessage) error { _, err := ParseURLIngestRequest(p); return err },
		`{"task_id":"t","status":"queued","colour":"red"}`: func(p json.RawMessage) error { _, err := ParseURLIngestResponse(p); return err },
		`{"id":"t","colour":"red"}`:                        func(p json.RawMessage) error { _, err := ParseTaskStatusRequest(p); return err },
		`{"id":"t","kind":"k","status":"s","created_at":"2026-10-23T09:15:00Z","updated_at":"2026-10-23T09:15:00Z","colour":"red"}`: func(p json.RawMessage) error { _, err := ParseTaskStatusResponse(p); return err },
	}
	for payload, parse := range payloads {
		err := parse(json.RawMessage(payload))
		assertInvalidPayload(t, err, payload)
		if strings.Contains(err.Error(), "red") {
			t.Errorf("the error repeats the value: %v", err)
		}
	}
}

func TestParseErrorsAreInvalidPayload(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":             "",
		"blank":             "  ",
		"null":              "null",
		"array":             "[]",
		"not JSON":          "{",
		"trailing data":     `{"query":"x"} {}`,
		"wrong type":        `{"query":5}`,
		"limit 21":          `{"query":"x","limit":21}`,
		"nonexistent month": `{"query":"x","start":"2026-13-01T00:00:00Z"}`,
		"nonexistent day":   `{"query":"x","end":"2026-02-30T00:00:00Z"}`,
		"huge number":       `{"query":"x","limit":1e999}`,
	} {
		_, err := ParseDocumentsSearchRequest(json.RawMessage(payload))
		assertInvalidPayload(t, err, name)
	}
}

func TestParseErrorNamesTheFieldNotTheValue(t *testing.T) {
	_, err := ParseURLIngestRequest(mustJSON(t, object{"url": "https://a.org/secret-token-123", "tags": []any{repeat("t", 65)}}))
	assertInvalidPayload(t, err, "tag too long")
	if !strings.Contains(err.Error(), "tags[0]") || strings.Contains(err.Error(), "secret-token-123") {
		t.Errorf("error: %v", err)
	}
}

func TestParseAcceptsAnExplicitZeroOffsetAndNoTags(t *testing.T) {
	got, err := ParseDocumentsSearchRequest(json.RawMessage(`{"query":"x","offset":0,"tags":[]}`))
	if err != nil || got.Offset != 0 || len(got.Tags) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func assertInvalidPayload(t *testing.T, err error, what string) {
	t.Helper()
	var verr *message.Error
	if !errors.As(err, &verr) || verr.Code != message.CodeInvalidPayload {
		t.Fatalf("%s: want *message.Error invalid_payload, got %T %v", what, err, err)
	}
	if !strings.HasPrefix(verr.Message, "payload is not valid: ") {
		t.Errorf("%s: message %q", what, verr.Message)
	}
}

package message

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestConstructorsDeriveTheRightFields(t *testing.T) {
	fixClock(t, testRequestID, testResponseID, "11111111-2222-4333-8444-555555555555", "22222222-3333-4444-8555-666666666666")

	req, err := NewRequest("assistant", "knowledge", "documents_search", map[string]any{"query": "probes"},
		WithContext("u-42", "t-1001"), WithSummary("Find probes"))
	if err != nil {
		t.Fatal(err)
	}
	want := validRequest()
	want.Context = &Context{UserID: "u-42", TaskID: "t-1001"}
	want.Summary = "Find probes"
	want.CreatedAt = "2026-10-23T09:15:00Z" // the clock is in +02:00; created_at is UTC
	if !reflect.DeepEqual(req, want) {
		t.Errorf("request:\n got %+v\nwant %+v", req, want)
	}

	reply, err := Reply(req, "knowledge", struct {
		Documents []string `json:"documents"`
	}{Documents: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		got  Envelope
		typ  Type
	}{{"reply", reply, TypeResponse}}
	event, err := Event(req, "knowledge", map[string]any{"step": "fetch"})
	if err != nil {
		t.Fatal(err)
	}
	checks = append(checks, struct {
		name string
		got  Envelope
		typ  Type
	}{"event", event, TypeEvent})
	failure := ErrorReply(req, "knowledge", CodeTimeout, "Too slow.", true)
	checks = append(checks, struct {
		name string
		got  Envelope
		typ  Type
	}{"error", failure, TypeError})

	for _, c := range checks {
		g := c.got
		if g.Type != c.typ || g.To != "assistant" || g.From != "knowledge" || g.CorrelationID != req.ID ||
			g.Operation != "documents_search" || g.ID == req.ID || g.SchemaVersion != 1 {
			t.Errorf("%s: %+v", c.name, g)
		}
		if g.Context == nil || *g.Context != *req.Context || g.Context == req.Context {
			t.Errorf("%s: context %+v must be a copy of the request's", c.name, g.Context)
		}
		if g.Summary != "" {
			t.Errorf("%s: summary %q must not be copied", c.name, g.Summary)
		}
		if err := Validate(g, nil); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if string(reply.Payload) != `{"documents":[]}` {
		t.Errorf("reply payload = %s", reply.Payload)
	}
	if failure.Error == nil || *failure.Error != (ErrorBody{Code: CodeTimeout, Message: "Too slow.", Retryable: true}) || failure.Payload != nil {
		t.Errorf("error reply = %+v", failure)
	}
}

func TestOptionsAndNilPayload(t *testing.T) {
	fixClock(t, testRequestID)
	req, err := NewRequest("assistant", "knowledge", "task_status", nil, WithContext("", ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(req.Payload) != "{}" || req.Context != nil {
		t.Errorf("nil payload must become {} and an empty context be left out: %+v", req)
	}
	if err := Validate(req, nil); err != nil {
		t.Error(err)
	}
	only, _ := NewRequest("a", "b", "c", nil, WithContext("u-1", ""))
	if only.Context == nil || only.Context.UserID != "u-1" || only.Context.TaskID != "" {
		t.Errorf("context = %+v", only.Context)
	}
}

func TestConstructorsReportAPayloadThatCannotBeEncoded(t *testing.T) {
	req := validRequest()
	bad := map[string]any{"f": func() {}}
	if _, err := NewRequest("a", "b", "c", bad); err == nil || !strings.Contains(err.Error(), `"c"`) {
		t.Errorf("NewRequest: %v", err)
	}
	if _, err := Reply(req, "knowledge", bad); err == nil {
		t.Error("Reply must fail")
	}
	if _, err := Event(req, "knowledge", bad); err == nil {
		t.Error("Event must fail")
	}
}

func TestNewUUIDIsAVersion4UUID(t *testing.T) {
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := newUUID()
		if !v4.MatchString(id) || seen[id] || !uuidRe.MatchString(id) {
			t.Fatalf("bad or repeated id %q", id)
		}
		seen[id] = true
	}
}

func TestRoundTrip(t *testing.T) {
	withContext := validRequest()
	withContext.Context = &Context{UserID: "u-42", TaskID: "t-1"}
	withContext.Summary = "Find <probes> & more"
	for name, env := range map[string]Envelope{
		"request": withContext, "response": validResponse(), "event": validEvent(), "error": validError(),
	} {
		data, err := Encode(env)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bytes.ContainsRune(data, '\n') {
			t.Errorf("%s: encoded form has a newline: %s", name, data)
		}
		got, err := Decode(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, env) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, env)
		}
	}
}

func TestEncodeKeepsThePayloadReadable(t *testing.T) {
	env := validRequest()
	env.Payload = json.RawMessage(`{"query": "a < b && c > d"}`)
	data, err := Encode(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"payload":{"query":"a < b && c > d"}`) {
		t.Errorf("payload text changed: %s", data)
	}
}

func TestEncodeRefusesWhatCannotBeSent(t *testing.T) {
	broken := validRequest()
	broken.Payload = json.RawMessage(`{not json`)
	huge := validRequest()
	huge.Payload = json.RawMessage(`{"text":"` + strings.Repeat("x", MaxEncodedBytes) + `"}`)
	for name, c := range map[string]struct {
		env  Envelope
		code string
	}{
		"payload is not JSON": {broken, CodeInvalidMessage},
		"over the limit":      {huge, CodeTooLarge},
	} {
		_, err := Encode(c.env)
		if e := codeOf(t, err); e.Code != c.code {
			t.Errorf("%s: %v", name, err)
		}
	}
	atLimit := validRequest()
	atLimit.Payload = json.RawMessage(`{"t":""}`)
	base, _ := Encode(atLimit)
	atLimit.Payload = json.RawMessage(`{"t":"` + strings.Repeat("x", MaxEncodedBytes-len(base)) + `"}`)
	data, err := Encode(atLimit)
	if err != nil || len(data) != MaxEncodedBytes {
		t.Errorf("an envelope of exactly the limit must pass: len=%d err=%v", len(data), err)
	}
}

func TestDecodeRejectsWhatItMust(t *testing.T) {
	good := string(readTestdata(t, "request_documents_search.json"))
	cases := []struct {
		name    string
		input   string
		code    string
		message string
	}{
		{"unknown top-level field", strings.Replace(good, `"operation"`, `"extra":1,"operation"`, 1), CodeInvalidMessage, msgNotAnEnvelope},
		{"unknown context field", strings.Replace(good, `"task_id"`, `"nope":"x","task_id"`, 1), CodeInvalidMessage, msgNotAnEnvelope},
		{"trailing data", good + `{}`, CodeInvalidMessage, "message has data after the envelope"},
		{"trailing junk", good + ` x`, CodeInvalidMessage, "message has data after the envelope"},
		{"wrong type", strings.Replace(good, `"schema_version":1`, `"schema_version":"1"`, 1), CodeInvalidMessage, msgNotAnEnvelope},
		{"not json", `hello`, CodeInvalidMessage, msgNotAnEnvelope},
		{"empty", ``, CodeInvalidMessage, msgNotAnEnvelope},
		{"array", `[]`, CodeInvalidMessage, msgNotAnEnvelope},
		{"truncated", good[:len(good)/2], CodeInvalidMessage, msgNotAnEnvelope},
	}
	for _, c := range cases {
		_, err := Decode([]byte(c.input))
		if e := codeOf(t, err); e.Code != c.code || e.Message != c.message {
			t.Errorf("%s: got %q %q", c.name, e.Code, e.Message)
		}
	}
}

const msgNotAnEnvelope = "message is not a valid envelope: it must be one JSON object with only the documented fields"

func TestDecodeChecksTheSizeBeforeParsing(t *testing.T) {
	// Junk that would fail to parse: the answer is still too_large, so the parser never ran.
	for _, size := range []int{MaxEncodedBytes + 1, 1 << 20} {
		_, err := Decode(bytes.Repeat([]byte("{"), size))
		e := codeOf(t, err)
		if e.Code != CodeTooLarge || e.Message != "message is larger than the maximum encoded size" {
			t.Errorf("size %d: %v", size, e)
		}
	}
	// Exactly at the limit is parsed (and here fails as junk, not as too large).
	_, err := Decode(bytes.Repeat([]byte("{"), MaxEncodedBytes))
	if e := codeOf(t, err); e.Code != CodeInvalidMessage {
		t.Errorf("at the limit: %v", e)
	}
}

func TestDecodeKeepsThePayloadBytes(t *testing.T) {
	payload := `{"b":  1, "a":[true,   null], "unknown_to_us": {"x": "<y>"}}`
	input := strings.Replace(string(readTestdata(t, "request_documents_search.json")), `{"query":"kubernetes liveness probe","tags":["ops"],"limit":5,"offset":0}`, payload, 1)
	env, err := Decode([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if string(env.Payload) != payload {
		t.Errorf("payload = %s", env.Payload)
	}
}

func TestErrorTextAndCodes(t *testing.T) {
	e := newError(CodeNotFound, "No such thing.")
	var err error = e
	if err.Error() != "not_found: No such thing." {
		t.Errorf("Error() = %q", err.Error())
	}
	if !errors.As(err, new(*Error)) {
		t.Error("errors.As must find *Error")
	}
	for _, code := range []string{CodeInvalidMessage, CodeUnknownOperation, CodeInvalidPayload, CodeNotFound, CodeTooLarge, CodeTimeout, CodeUnavailable, CodeInternal} {
		if !knownCodes[code] {
			t.Errorf("%q is not known", code)
		}
	}
	if len(knownCodes) != 8 {
		t.Errorf("there must be eight codes, got %d", len(knownCodes))
	}
}

func TestEnvelopeSchemaReturnsACopy(t *testing.T) {
	a := EnvelopeSchema()
	if len(a) == 0 || !json.Valid(a) {
		t.Fatal("schema is empty or not JSON")
	}
	a[0] = 'X'
	if EnvelopeSchema()[0] == 'X' {
		t.Error("EnvelopeSchema must return a copy")
	}
}

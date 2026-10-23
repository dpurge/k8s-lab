package message

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidSamplesOfEachType(t *testing.T) {
	for name, env := range map[string]Envelope{
		"request":              validRequest(),
		"response":             validResponse(),
		"event":                validEvent(),
		"error":                validError(),
		"response no payload":  func() Envelope { e := validResponse(); e.Payload = nil; return e }(),
		"fractional seconds":   func() Envelope { e := validRequest(); e.CreatedAt = "2026-10-23T09:15:00.123456789Z"; return e }(),
		"context and summary":  func() Envelope { e := validRequest(); e.Context = &Context{UserID: "u"}; e.Summary = "s"; return e }(),
		"empty payload object": func() Envelope { e := validRequest(); e.Payload = json.RawMessage(`{}`); return e }(),
	} {
		if err := Validate(env, nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, file := range []string{"request_documents_search.json", "response_documents_search.json", "error_not_found.json"} {
		env, err := Decode(readTestdata(t, file))
		if err == nil {
			err = Validate(env, nil)
		}
		if err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
}

func TestValidateEnvelopeRules(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name    string
		change  func(*Envelope)
		message string
	}{
		{"schema_version 2", func(e *Envelope) { e.SchemaVersion = 2 }, "schema_version must be 1"},
		{"schema_version missing", func(e *Envelope) { e.SchemaVersion = 0 }, "schema_version must be 1"},
		{"id empty", func(e *Envelope) { e.ID = "" }, "id must be a lower-case UUID"},
		{"id not a UUID", func(e *Envelope) { e.ID = "abc" }, "id must be a lower-case UUID"},
		{"id upper case", func(e *Envelope) { e.ID = strings.ToUpper(testRequestID) }, "id must be a lower-case UUID"},
		{"type unknown", func(e *Envelope) { e.Type = "command" }, "type must be request, response, event or error"},
		{"type empty", func(e *Envelope) { e.Type = "" }, "type must be request, response, event or error"},
		{"correlation_id empty", func(e *Envelope) { e.CorrelationID = "" }, "correlation_id must be a lower-case UUID"},
		{"correlation_id bad", func(e *Envelope) { e.CorrelationID = "x" }, "correlation_id must be a lower-case UUID"},
		{"request correlation differs from id", func(e *Envelope) { e.CorrelationID = testResponseID }, "a request's correlation_id must equal its id"},
		{"from empty", func(e *Envelope) { e.From = "" }, "from must be 1 to 64 characters of a-z, 0-9, _ and -"},
		{"from upper case", func(e *Envelope) { e.From = "Assistant" }, "from must be 1 to 64 characters of a-z, 0-9, _ and -"},
		{"from too long", func(e *Envelope) { e.From = long(65) }, "from must be 1 to 64 characters of a-z, 0-9, _ and -"},
		{"from has a space", func(e *Envelope) { e.From = "my app" }, "from must be 1 to 64 characters of a-z, 0-9, _ and -"},
		{"to empty", func(e *Envelope) { e.To = "" }, "to must be 1 to 64 characters of a-z, 0-9, _ and -"},
		{"to too long", func(e *Envelope) { e.To = long(65) }, "to must be 1 to 64 characters of a-z, 0-9, _ and -"},
		{"to trailing newline", func(e *Envelope) { e.To = "knowledge\n" }, "to must be 1 to 64 characters of a-z, 0-9, _ and -"},
		{"created_at empty", func(e *Envelope) { e.CreatedAt = "" }, "created_at must be an RFC 3339 time in UTC, ending in Z"},
		{"created_at with offset", func(e *Envelope) { e.CreatedAt = "2026-10-23T11:15:00+02:00" }, "created_at must be an RFC 3339 time in UTC, ending in Z"},
		{"created_at zero offset", func(e *Envelope) { e.CreatedAt = "2026-10-23T09:15:00+00:00" }, "created_at must be an RFC 3339 time in UTC, ending in Z"},
		{"created_at not a time", func(e *Envelope) { e.CreatedAt = "2026-13-45T09:15:00Z" }, "created_at must be an RFC 3339 time in UTC, ending in Z"},
		{"created_at date only", func(e *Envelope) { e.CreatedAt = "2026-10-23" }, "created_at must be an RFC 3339 time in UTC, ending in Z"},
		{"operation empty", func(e *Envelope) { e.Operation = "" }, "operation must be 1 to 64 characters, start with a-z and use only a-z, 0-9 and _"},
		{"operation upper case", func(e *Envelope) { e.Operation = "Search" }, "operation must be 1 to 64 characters, start with a-z and use only a-z, 0-9 and _"},
		{"operation starts with a digit", func(e *Envelope) { e.Operation = "1search" }, "operation must be 1 to 64 characters, start with a-z and use only a-z, 0-9 and _"},
		{"operation has a dash", func(e *Envelope) { e.Operation = "documents-search" }, "operation must be 1 to 64 characters, start with a-z and use only a-z, 0-9 and _"},
		{"operation too long", func(e *Envelope) { e.Operation = long(65) }, "operation must be 1 to 64 characters, start with a-z and use only a-z, 0-9 and _"},
		{"summary too long", func(e *Envelope) { e.Summary = strings.Repeat("é", 501) }, "summary must be at most 500 characters"},
		{"request without payload", func(e *Envelope) { e.Payload = nil }, "a request must have a payload object"},
		{"payload null", func(e *Envelope) { e.Payload = json.RawMessage(`null`) }, "payload must be a JSON object"},
		{"payload array", func(e *Envelope) { e.Payload = json.RawMessage(`[1]`) }, "payload must be a JSON object"},
		{"payload string", func(e *Envelope) { e.Payload = json.RawMessage(`"x"`) }, "payload must be a JSON object"},
		{"payload not JSON", func(e *Envelope) { e.Payload = json.RawMessage(`{x`) }, "payload must be a JSON object"},
		{"request with error", func(e *Envelope) { e.Error = &ErrorBody{Code: CodeInternal, Message: "x"} }, "error is only allowed on a message of type error"},
	}
	for _, c := range cases {
		env := validRequest()
		c.change(&env)
		err := Validate(env, nil)
		if e := codeOf(t, err); e.Code != CodeInvalidMessage || e.Message != c.message {
			t.Errorf("%s: got %q %q", c.name, e.Code, e.Message)
		}
	}
}

func TestValidateResponseEventAndErrorRules(t *testing.T) {
	cases := []struct {
		name    string
		env     Envelope
		message string
	}{
		{"response with error", func() Envelope {
			e := validResponse()
			e.Error = &ErrorBody{Code: CodeInternal, Message: "x"}
			return e
		}(), "error is only allowed on a message of type error"},
		{"event with error", func() Envelope { e := validEvent(); e.Error = &ErrorBody{Code: CodeInternal, Message: "x"}; return e }(), "error is only allowed on a message of type error"},
		{"response without correlation_id", func() Envelope { e := validResponse(); e.CorrelationID = ""; return e }(), "correlation_id must be a lower-case UUID"},
		{"event without operation", func() Envelope { e := validEvent(); e.Operation = ""; return e }(), "operation must be 1 to 64 characters, start with a-z and use only a-z, 0-9 and _"},
		{"error without correlation_id", func() Envelope { e := validError(); e.CorrelationID = "nope"; return e }(), "correlation_id must be a lower-case UUID"},
		{"error without operation", func() Envelope { e := validError(); e.Operation = ""; return e }(), "operation must be 1 to 64 characters, start with a-z and use only a-z, 0-9 and _"},
		{"error without error object", func() Envelope { e := validError(); e.Error = nil; return e }(), "a message of type error must have an error object"},
		{"error with unknown code", func() Envelope { e := validError(); e.Error.Code = "boom"; return e }(), "error.code must be one of the documented codes"},
		{"error with empty code", func() Envelope { e := validError(); e.Error.Code = ""; return e }(), "error.code must be one of the documented codes"},
		{"error with empty message", func() Envelope { e := validError(); e.Error.Message = ""; return e }(), "error.message must not be empty"},
		{"error with long message", func() Envelope { e := validError(); e.Error.Message = strings.Repeat("m", 1001); return e }(), "error.message must be at most 1000 characters"},
		{"response payload array", func() Envelope { e := validResponse(); e.Payload = json.RawMessage(`[]`); return e }(), "payload must be a JSON object"},
	}
	for _, c := range cases {
		err := Validate(c.env, nil)
		if e := codeOf(t, err); e.Code != CodeInvalidMessage || e.Message != c.message {
			t.Errorf("%s: got %q %q", c.name, e.Code, e.Message)
		}
	}
	atLimit := validError()
	atLimit.Error.Message = strings.Repeat("m", 1000)
	if err := Validate(atLimit, nil); err != nil {
		t.Errorf("a 1000 character message must pass: %v", err)
	}
}

func TestValidateWithARegistry(t *testing.T) {
	reg := searchRegistry(t)

	if err := Validate(validRequest(), reg); err != nil {
		t.Errorf("valid request: %v", err)
	}
	ok := validResponse()
	ok.Payload = json.RawMessage(`{"documents":[]}`)
	if err := Validate(ok, reg); err != nil {
		t.Errorf("valid response: %v", err)
	}

	unknown := validRequest()
	unknown.Operation = "url_ingest"
	if e := codeOf(t, Validate(unknown, reg)); e.Code != CodeUnknownOperation || e.Message != "the operation is not known to the receiver" {
		t.Errorf("unknown request: %v", e)
	}
	// A response, event or error for an operation this registry lacks has no schema to break.
	unknownResponse := validResponse()
	unknownResponse.Operation = "url_ingest"
	if err := Validate(unknownResponse, reg); err != nil {
		t.Errorf("unknown response: %v", err)
	}

	badRequest := validRequest()
	badRequest.Payload = json.RawMessage(`{"query":"","limit":5}`)
	e := codeOf(t, Validate(badRequest, reg))
	if e.Code != CodeInvalidPayload || !strings.HasPrefix(e.Message, "payload is not valid: query: ") {
		t.Errorf("bad request payload: %v", e)
	}
	badResponse := validResponse()
	badResponse.Payload = json.RawMessage(`{"documents":"none"}`)
	if e := codeOf(t, Validate(badResponse, reg)); e.Code != CodeInvalidPayload || !strings.Contains(e.Message, "documents: ") {
		t.Errorf("bad response payload: %v", e)
	}
	missing := validResponse()
	missing.Payload = nil
	if e := codeOf(t, Validate(missing, reg)); e.Code != CodeInvalidPayload {
		t.Errorf("a response without a payload is checked as {}: %v", e)
	}
	for _, env := range []Envelope{validEvent(), validError()} {
		env.Payload = json.RawMessage(`{"anything":1}`)
		if err := Validate(env, reg); err != nil {
			t.Errorf("%s carries no schema: %v", env.Type, err)
		}
	}
	// The envelope rules come first.
	broken := validRequest()
	broken.Operation = "Nope"
	if e := codeOf(t, Validate(broken, reg)); e.Code != CodeInvalidMessage {
		t.Errorf("broken envelope: %v", e)
	}
}

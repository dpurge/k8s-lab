package message

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// compiledEnvelopeSchema compiles the embedded schema with the same library the
// registry uses.
func compiledEnvelopeSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(EnvelopeSchema()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("envelope.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("envelope.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

type object = map[string]any

// sample builds an envelope as a JSON object: base with the changes applied; a
// nil change value deletes the key.
func sample(base object, changes object) []byte {
	out := object{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range changes {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	data, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	return data
}

var (
	baseRequest = object{
		"schema_version": 1, "id": testRequestID, "type": "request", "correlation_id": testRequestID,
		"from": "assistant", "to": "knowledge", "created_at": "2026-10-23T09:15:00Z", "operation": "documents_search",
		"payload": object{"query": "probes"},
	}
	baseResponse = object{
		"schema_version": 1, "id": testResponseID, "type": "response", "correlation_id": testRequestID,
		"from": "knowledge", "to": "assistant", "created_at": "2026-10-23T09:15:01Z", "operation": "documents_search",
		"payload": object{"documents": []any{}},
	}
	baseError = object{
		"schema_version": 1, "id": testResponseID, "type": "error", "correlation_id": testRequestID,
		"from": "knowledge", "to": "assistant", "created_at": "2026-10-23T09:15:01Z", "operation": "documents_search",
		"error": object{"code": "not_found", "message": "No document has that id.", "retryable": false},
	}
)

// verdictSamples are envelopes as JSON. The rules JSON Schema cannot express
// are left out on purpose: that created_at is a real time (the schema checks
// only its shape), that a request's correlation_id equals its id, and the JSON
// cases where Go and the schema read the same text differently (a null context
// or error, and 1.0 as a number). Those are covered by Go-only tests.
func verdictSamples() []struct {
	name  string
	json  []byte
	valid bool
} {
	longName := strings.Repeat("a", 65)
	return []struct {
		name  string
		json  []byte
		valid bool
	}{
		{"request", sample(baseRequest, nil), true},
		{"response", sample(baseResponse, nil), true},
		{"event", sample(baseResponse, object{"type": "event"}), true},
		{"error", sample(baseError, nil), true},
		{"error without retryable", sample(baseError, object{"error": object{"code": "internal", "message": "x"}}), true},
		{"response without payload", sample(baseResponse, object{"payload": nil}), true},
		{"request with context and summary", sample(baseRequest, object{"context": object{"user_id": "u", "task_id": "t"}, "summary": "hi"}), true},
		{"empty context", sample(baseRequest, object{"context": object{}}), true},
		{"fractional seconds", sample(baseRequest, object{"created_at": "2026-10-23T09:15:00.5Z"}), true},
		{"summary of 500 characters", sample(baseRequest, object{"summary": strings.Repeat("é", 500)}), true},
		{"name of 64 characters", sample(baseRequest, object{"to": strings.Repeat("a", 64)}), true},
		{"error message of 1000 characters", sample(baseError, object{"error": object{"code": "timeout", "message": strings.Repeat("m", 1000), "retryable": true}}), true},

		{"schema_version 2", sample(baseRequest, object{"schema_version": 2}), false},
		{"schema_version as text", sample(baseRequest, object{"schema_version": "1"}), false},
		{"schema_version missing", sample(baseRequest, object{"schema_version": nil}), false},
		{"id missing", sample(baseRequest, object{"id": nil}), false},
		{"id not a UUID", sample(baseRequest, object{"id": "abc"}), false},
		{"id upper case", sample(baseRequest, object{"id": strings.ToUpper(testRequestID)}), false},
		{"type unknown", sample(baseRequest, object{"type": "command"}), false},
		{"type missing", sample(baseRequest, object{"type": nil}), false},
		{"correlation_id missing", sample(baseResponse, object{"correlation_id": nil}), false},
		{"correlation_id bad", sample(baseResponse, object{"correlation_id": "x"}), false},
		{"from missing", sample(baseRequest, object{"from": nil}), false},
		{"from upper case", sample(baseRequest, object{"from": "Assistant"}), false},
		{"from too long", sample(baseRequest, object{"from": longName}), false},
		{"to empty", sample(baseRequest, object{"to": ""}), false},
		{"created_at with offset", sample(baseRequest, object{"created_at": "2026-10-23T11:15:00+02:00"}), false},
		{"created_at date only", sample(baseRequest, object{"created_at": "2026-10-23"}), false},
		{"created_at missing", sample(baseRequest, object{"created_at": nil}), false},
		{"operation missing", sample(baseRequest, object{"operation": nil}), false},
		{"operation upper case", sample(baseRequest, object{"operation": "Search"}), false},
		{"operation too long", sample(baseRequest, object{"operation": longName}), false},
		{"summary of 501 characters", sample(baseRequest, object{"summary": strings.Repeat("é", 501)}), false},
		{"summary not text", sample(baseRequest, object{"summary": 5}), false},
		{"context with unknown key", sample(baseRequest, object{"context": object{"tenant": "x"}}), false},
		{"context user_id not text", sample(baseRequest, object{"context": object{"user_id": 5}}), false},
		{"request without payload", sample(baseRequest, object{"payload": nil}), false},
		{"payload null", sample(baseRequest, object{"payload": json.RawMessage("null")}), false},
		{"payload array", sample(baseRequest, object{"payload": []any{1}}), false},
		{"payload text", sample(baseResponse, object{"payload": "x"}), false},
		{"unknown top-level field", sample(baseRequest, object{"extra": true}), false},
		{"error on a request", sample(baseRequest, object{"error": object{"code": "internal", "message": "x"}}), false},
		{"error on a response", sample(baseResponse, object{"error": object{"code": "internal", "message": "x"}}), false},
		{"error on an event", sample(baseResponse, object{"type": "event", "error": object{"code": "internal", "message": "x"}}), false},
		{"error type without error", sample(baseError, object{"error": nil}), false},
		{"error code unknown", sample(baseError, object{"error": object{"code": "boom", "message": "x"}}), false},
		{"error message empty", sample(baseError, object{"error": object{"code": "internal", "message": ""}}), false},
		{"error message of 1001 characters", sample(baseError, object{"error": object{"code": "internal", "message": strings.Repeat("m", 1001)}}), false},
		{"error without code", sample(baseError, object{"error": object{"message": "x"}}), false},
		{"error with unknown key", sample(baseError, object{"error": object{"code": "internal", "message": "x", "detail": 1}}), false},
		{"error retryable not a boolean", sample(baseError, object{"error": object{"code": "internal", "message": "x", "retryable": "yes"}}), false},
		{"empty object", []byte(`{}`), false},
	}
}

// goVerdict is what a receiver does: Decode, then Validate without a registry.
func goVerdict(data []byte) error {
	env, err := Decode(data)
	if err != nil {
		return err
	}
	return Validate(env, nil)
}

func TestGoValidationAndTheJSONSchemaAgree(t *testing.T) {
	schema := compiledEnvelopeSchema(t)
	samples := verdictSamples()
	if len(samples) < 30 {
		t.Fatalf("need at least 30 samples, have %d", len(samples))
	}
	var valid, invalid int
	for _, s := range samples {
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(s.json))
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		goOK := goVerdict(s.json) == nil
		schemaOK := schema.Validate(instance) == nil
		if goOK != schemaOK || goOK != s.valid {
			t.Errorf("%s: Go says valid=%v, schema says valid=%v, expected valid=%v\n%s", s.name, goOK, schemaOK, s.valid, s.json)
		}
		if s.valid {
			valid++
		} else {
			invalid++
		}
	}
	t.Logf("%d samples: %d valid, %d invalid", len(samples), valid, invalid)
}

func TestTestdataEnvelopesPassTheSchemaToo(t *testing.T) {
	schema := compiledEnvelopeSchema(t)
	for _, file := range []string{"request_documents_search.json", "response_documents_search.json", "error_not_found.json"} {
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(readTestdata(t, file)))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
}

// What the schema cannot say is checked by Go alone; the schema is looser there.
func TestRulesOnlyGoChecks(t *testing.T) {
	schema := compiledEnvelopeSchema(t)
	for name, data := range map[string][]byte{
		"request correlation_id differs from id": sample(baseRequest, object{"correlation_id": testResponseID}),
		"created_at that is not a time":          sample(baseRequest, object{"created_at": "2026-13-45T09:15:00Z"}),
	} {
		instance, _ := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err := schema.Validate(instance); err != nil {
			t.Errorf("%s: the schema is expected to accept it: %v", name, err)
		}
		if e := codeOf(t, goVerdict(data)); e.Code != CodeInvalidMessage {
			t.Errorf("%s: Go must reject it: %v", name, e)
		}
	}
}

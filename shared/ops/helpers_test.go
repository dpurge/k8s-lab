package ops

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type object = map[string]any

// compileSchema compiles raw with the same library the registry uses.
func compileSchema(t *testing.T, name string, raw json.RawMessage) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(name+".json", doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	schema, err := compiler.Compile(name + ".json")
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return schema
}

// schemaAccepts reports whether the schema accepts the JSON text.
func schemaAccepts(t *testing.T, schema *jsonschema.Schema, data []byte) bool {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("sample is not JSON: %v: %s", err, data)
	}
	return schema.Validate(instance) == nil
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// with returns base with the changes applied; a nil change value deletes the key.
func with(base object, changes object) object {
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
	return out
}

func repeat(s string, n int) string { return strings.Repeat(s, n) }

func tagList(n int) []any {
	tags := make([]any, n)
	for i := range tags {
		tags[i] = "t"
	}
	return tags
}

func documents(n int) []any {
	docs := make([]any, n)
	for i := range docs {
		docs[i] = with(baseDocument, nil)
	}
	return docs
}

var (
	baseDocument = object{"id": "d-17", "title": "Probes", "summary": "Liveness and readiness probes.", "tags": []any{"ops"}, "score": 0.82, "url": "/documents/d-17"}

	baseSearchRequest  = object{"query": "kubernetes liveness probe"}
	baseSearchResponse = object{"documents": []any{baseDocument}}
	baseIngestRequest  = object{"url": "https://wiki.example/probes"}
	baseIngestResponse = object{"task_id": "t-1001", "status": "queued"}
	baseStatusRequest  = object{"id": "t-1001"}
	baseStatusResponse = object{"id": "t-1001", "kind": "url_ingest", "status": "running", "created_at": "2026-10-23T09:15:00Z", "updated_at": "2026-10-23T09:15:30.5Z"}
)

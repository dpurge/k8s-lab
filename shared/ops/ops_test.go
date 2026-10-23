package ops

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"k8s-lab/shared/message"
)

func TestEverySchemaCompiles(t *testing.T) {
	for _, name := range Names() {
		request, response, found := Schemas(name)
		if !found {
			t.Fatalf("Schemas(%q) not found", name)
		}
		compileSchema(t, name+".request", request)
		compileSchema(t, name+".response", response)
		for _, raw := range []json.RawMessage{request, response} {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
				t.Errorf("%s: $schema is %v", name, doc["$schema"])
			}
			if doc["additionalProperties"] != false {
				t.Errorf("%s: additionalProperties is %v, want false", name, doc["additionalProperties"])
			}
		}
	}
}

func TestSchemasUnknownName(t *testing.T) {
	request, response, found := Schemas("documents_delete")
	if found || request != nil || response != nil {
		t.Fatalf("want not found, got %v %s %s", found, request, response)
	}
}

func TestNamesIsACopy(t *testing.T) {
	names := Names()
	if !reflect.DeepEqual(names, []string{"documents_search", "url_ingest", "task_status"}) {
		t.Fatalf("names: %v", names)
	}
	names[0] = "changed"
	if Names()[0] != DocumentsSearch {
		t.Fatal("Names returned the catalogue itself")
	}
}

func TestRegisterAllWhenNoNamesAreGiven(t *testing.T) {
	reg := message.NewRegistry()
	if err := Register(reg); err != nil {
		t.Fatal(err)
	}
	want := []string{DocumentsSearch, TaskStatus, URLIngest}
	if got := reg.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registered %v, want %v", got, want)
	}
}

func TestRegisterOnlyTheNamedOperations(t *testing.T) {
	reg := message.NewRegistry()
	if err := Register(reg, URLIngest, TaskStatus); err != nil {
		t.Fatal(err)
	}
	if reg.Has(DocumentsSearch) || !reg.Has(URLIngest) || !reg.Has(TaskStatus) {
		t.Fatalf("registered %v", reg.Names())
	}
}

func TestRegisterUnknownNameFailsAndRegistersNothing(t *testing.T) {
	reg := message.NewRegistry()
	err := Register(reg, DocumentsSearch, "documents_delete")
	if err == nil || !strings.Contains(err.Error(), "documents_delete") {
		t.Fatalf("want an error naming the operation, got %v", err)
	}
	if names := reg.Names(); len(names) != 0 {
		t.Fatalf("registered %v before failing", names)
	}
}

func TestRegisterTwiceFails(t *testing.T) {
	reg := message.NewRegistry()
	if err := Register(reg, TaskStatus); err != nil {
		t.Fatal(err)
	}
	if err := Register(reg, TaskStatus); err == nil {
		t.Fatal("want an error for a second registration")
	}
}

// TestEnvelopesValidateForEveryOperation builds a request and a response with
// message.NewRequest and message.Reply from the Go types and checks both with
// message.Validate against the registry.
func TestEnvelopesValidateForEveryOperation(t *testing.T) {
	cases := []struct {
		operation string
		request   any
		response  any
	}{
		{DocumentsSearch,
			DocumentsSearchRequest{Query: "probes", Tags: []string{"ops"}, Start: "2026-01-01T00:00:00Z", Limit: 5, Offset: 0},
			DocumentsSearchResponse{Documents: []Document{{ID: "d-17", Title: "Probes", Summary: "s", Tags: []string{}, Score: 0.5, URL: "/d/17"}}, Total: 1}},
		{URLIngest,
			URLIngestRequest{URL: "https://wiki.example/probes", Tags: []string{"ops"}},
			URLIngestResponse{TaskID: "t-1", Status: "queued"}},
		{TaskStatus,
			TaskStatusRequest{ID: "t-1"},
			TaskStatusResponse{ID: "t-1", Kind: "url_ingest", Status: "running", Step: "fetch", CreatedAt: "2026-10-23T09:15:00Z", UpdatedAt: "2026-10-23T09:15:01Z"}},
	}
	reg := message.NewRegistry()
	if err := Register(reg); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.operation, func(t *testing.T) {
			req, err := message.NewRequest("assistant", "knowledge", c.operation, c.request)
			if err != nil {
				t.Fatal(err)
			}
			if err := message.Validate(req, reg); err != nil {
				t.Fatalf("request: %v", err)
			}
			resp, err := message.Reply(req, "knowledge", c.response)
			if err != nil {
				t.Fatal(err)
			}
			if err := message.Validate(resp, reg); err != nil {
				t.Fatalf("response: %v", err)
			}
		})
	}
}

func TestInvalidPayloadIsRefusedByMessageValidate(t *testing.T) {
	reg := message.NewRegistry()
	if err := Register(reg); err != nil {
		t.Fatal(err)
	}
	req, err := message.NewRequest("assistant", "knowledge", DocumentsSearch, object{"query": "x", "limit": 21})
	if err != nil {
		t.Fatal(err)
	}
	var verr *message.Error
	if err := message.Validate(req, reg); !errors.As(err, &verr) || verr.Code != message.CodeInvalidPayload {
		t.Fatalf("want invalid_payload, got %v", err)
	}
}

// TestTheWireRefusesTheZeroValuesGoCannotSee covers what the verdict table
// leaves out: an optional field written with its zero value, a null, and a
// document without a field.
func TestTheWireRefusesTheZeroValuesGoCannotSee(t *testing.T) {
	request, response, _ := Schemas(DocumentsSearch)
	requestSchema := compileSchema(t, "documents_search.request", request)
	responseSchema := compileSchema(t, "documents_search.response", response)
	for name, v := range map[string]object{
		"limit 0":     with(baseSearchRequest, object{"limit": 0}),
		"start empty": with(baseSearchRequest, object{"start": ""}),
		"end empty":   with(baseSearchRequest, object{"end": ""}),
		"tags null":   with(baseSearchRequest, object{"tags": json.RawMessage("null")}),
	} {
		if schemaAccepts(t, requestSchema, mustJSON(t, v)) {
			t.Errorf("request schema accepts %s", name)
		}
	}
	for _, field := range []string{"title", "summary", "url", "score"} {
		doc := with(baseDocument, object{field: nil})
		if schemaAccepts(t, responseSchema, mustJSON(t, object{"documents": []any{doc}})) {
			t.Errorf("response schema accepts a document without %s", field)
		}
	}
}

func TestTaskStatusResponseNeverAcceptsPayloadOrResult(t *testing.T) {
	_, response, _ := Schemas(TaskStatus)
	schema := compileSchema(t, "task_status.response", response)
	var doc struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(response, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.AdditionalProperties {
		t.Fatal("the response schema allows additional properties")
	}
	for _, forbidden := range []string{"payload", "result"} {
		if _, has := doc.Properties[forbidden]; has {
			t.Errorf("the response schema declares %q", forbidden)
		}
		if schemaAccepts(t, schema, mustJSON(t, with(baseStatusResponse, object{forbidden: object{"text": "user text"}}))) {
			t.Errorf("schema accepts %q", forbidden)
		}
		if _, err := ParseTaskStatusResponse(mustJSON(t, with(baseStatusResponse, object{forbidden: "x"}))); err == nil {
			t.Errorf("ParseTaskStatusResponse accepts %q", forbidden)
		}
		if _, has := reflect.TypeOf(TaskStatusResponse{}).FieldByNameFunc(func(n string) bool { return strings.EqualFold(n, forbidden) }); has {
			t.Errorf("TaskStatusResponse has a %s field", forbidden)
		}
	}
}

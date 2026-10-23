package message

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const (
	searchRequestSchema  = `{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1}}}`
	searchResponseSchema = `{"type":"object","required":["documents"],"properties":{"documents":{"type":"array"}}}`
)

func searchRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry()
	err := reg.Register(Operation{Name: "documents_search", Request: json.RawMessage(searchRequestSchema), Response: json.RawMessage(searchResponseSchema)})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestRegistryValidatesRequestAndResponsePayloads(t *testing.T) {
	reg := searchRegistry(t)
	cases := []struct {
		name     string
		validate func(string, json.RawMessage) error
		payload  string
		want     string // "" for valid, else the exact message
	}{
		{"request ok", reg.ValidateRequestPayload, `{"query":"probes","limit":3}`, ""},
		{"request empty query", reg.ValidateRequestPayload, `{"query":""}`, "payload is not valid: query: minLength: got 0, want 1"},
		{"request wrong type", reg.ValidateRequestPayload, `{"query":5}`, "payload is not valid: query: got number, want string"},
		{"request nested path", reg.ValidateRequestPayload, `{"query":"x","limit":0}`, "payload is not valid: limit: minimum: got 0, want 1"},
		{"request unknown key", reg.ValidateRequestPayload, `{"query":"x","bogus":1}`, "payload is not valid: (root): additional properties 'bogus' not allowed"},
		{"request missing key", reg.ValidateRequestPayload, `{}`, "payload is not valid: (root): missing property 'query'"},
		{"request not JSON", reg.ValidateRequestPayload, `{`, "payload is not valid JSON"},
		{"response ok", reg.ValidateResponsePayload, `{"documents":[]}`, ""},
		{"response wrong", reg.ValidateResponsePayload, `{"documents":{}}`, "payload is not valid: documents: got object, want array"},
	}
	for _, c := range cases {
		err := c.validate("documents_search", json.RawMessage(c.payload))
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		e := codeOf(t, err)
		if e.Code != CodeInvalidPayload || e.Message != c.want {
			t.Errorf("%s: got %q %q", c.name, e.Code, e.Message)
		}
	}
}

func TestRegistryReportsEveryViolationSorted(t *testing.T) {
	err := searchRegistry(t).ValidateRequestPayload("documents_search", json.RawMessage(`{"query":"","limit":0}`))
	e := codeOf(t, err)
	want := "payload is not valid: limit: minimum: got 0, want 1; query: minLength: got 0, want 1"
	if e.Message != want {
		t.Errorf("message = %q", e.Message)
	}
}

func TestRegistryUnknownOperation(t *testing.T) {
	reg := searchRegistry(t)
	for _, validate := range []func(string, json.RawMessage) error{reg.ValidateRequestPayload, reg.ValidateResponsePayload} {
		e := codeOf(t, validate("nope", json.RawMessage(`{}`)))
		if e.Code != CodeUnknownOperation || e.Message != "the operation is not known to the receiver" {
			t.Errorf("got %v", e)
		}
	}
}

func TestRegistryLookups(t *testing.T) {
	reg := searchRegistry(t)
	schema := json.RawMessage(`{"type":"object"}`)
	if err := reg.Register(Operation{Name: "a_first", Request: schema, Response: schema}); err != nil {
		t.Fatal(err)
	}
	if !reg.Has("documents_search") || reg.Has("nope") {
		t.Error("Has is wrong")
	}
	if got := reg.Names(); !reflect.DeepEqual(got, []string{"a_first", "documents_search"}) {
		t.Errorf("Names = %v", got)
	}
	if got := NewRegistry().Names(); len(got) != 0 {
		t.Errorf("empty registry: %v", got)
	}
}

func TestRegisterRejectsWhatItCannotUse(t *testing.T) {
	ok := json.RawMessage(`{"type":"object"}`)
	reg := searchRegistry(t)
	cases := []struct {
		name string
		op   Operation
		want string
	}{
		{"bad name", Operation{Name: "Bad-Name", Request: ok, Response: ok}, `register operation "Bad-Name": the name must match`},
		{"empty name", Operation{Request: ok, Response: ok}, `register operation "": the name must match`},
		{"duplicate", Operation{Name: "documents_search", Request: ok, Response: ok}, `register operation "documents_search": already registered`},
		{"request does not compile", Operation{Name: "broken", Request: json.RawMessage(`{"type":"banana"}`), Response: ok}, `register operation "broken": compile request schema`},
		{"response does not compile", Operation{Name: "broken", Request: ok, Response: json.RawMessage(`{"type":"banana"}`)}, `register operation "broken": compile response schema`},
		{"request is not JSON", Operation{Name: "broken", Request: json.RawMessage(`{not json`), Response: ok}, `register operation "broken": parse request schema`},
		{"response missing", Operation{Name: "broken", Request: ok}, `register operation "broken": parse response schema`},
	}
	for _, c := range cases {
		err := reg.Register(c.op)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	if reg.Has("broken") {
		t.Error("a failed registration must leave nothing behind")
	}
}

func TestRegistryIsSafeForConcurrentUse(t *testing.T) {
	reg := searchRegistry(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = reg.ValidateRequestPayload("documents_search", json.RawMessage(`{"query":"x"}`))
				_ = reg.Has("documents_search")
				_ = reg.Names()
			}
		}()
	}
	wg.Wait()
}

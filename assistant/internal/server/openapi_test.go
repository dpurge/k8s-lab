package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"assistant/schemas"
)

// spec is schemas/api/openapi.yaml, loaded so tests can hold the server to it:
// the router must have exactly the documented routes, and every response a test
// provokes must be documented and match its schema.
type spec struct {
	t        *testing.T
	doc      map[string]any
	compiler *jsonschema.Compiler
	compiled map[string]*jsonschema.Schema
}

const specURL = "openapi.yaml"

func loadSpec(t *testing.T) *spec {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(schemas.OpenAPI, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	asJSON, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("openapi.yaml cannot be expressed as JSON: %v", err)
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(specURL, parsed); err != nil {
		t.Fatal(err)
	}
	return &spec{t: t, doc: doc, compiler: c, compiled: map[string]*jsonschema.Schema{}}
}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// operations returns "METHOD /path" for every documented operation.
func (s *spec) operations() map[string]bool {
	out := map[string]bool{}
	paths, _ := s.doc["paths"].(map[string]any)
	for path, item := range paths {
		for _, m := range httpMethods {
			if _, ok := item.(map[string]any)[m]; ok {
				out[strings.ToUpper(m)+" "+path] = true
			}
		}
	}
	return out
}

func (s *spec) operation(method, path string) map[string]any {
	paths, _ := s.doc["paths"].(map[string]any)
	item, _ := paths[path].(map[string]any)
	op, _ := item[strings.ToLower(method)].(map[string]any)
	return op
}

// check fails the test unless status is documented for the operation and, for
// a JSON response, the body matches the documented schema.
func (s *spec) check(method, path string, status int, contentType string, body []byte) {
	s.t.Helper()
	op := s.operation(method, path)
	if op == nil {
		s.t.Errorf("%s %s is not documented in openapi.yaml", method, path)
		return
	}
	responses, _ := op["responses"].(map[string]any)
	resp := s.resolve(responses[strconv.Itoa(status)])
	if resp == nil {
		s.t.Errorf("openapi.yaml does not document status %d for %s %s", status, method, path)
		return
	}
	content, _ := resp["content"].(map[string]any)
	if content == nil {
		return // documented as having no body
	}
	mediaType := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	media, ok := content[mediaType].(map[string]any)
	if !ok {
		s.t.Errorf("%s %s %d: openapi.yaml documents no %q response (documented: %v)", method, path, status, mediaType, keys(content))
		return
	}
	if mediaType != "application/json" {
		return
	}
	schema := s.schemaFor(method, path, status, mediaType, media)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		s.t.Errorf("%s %s %d: body is not JSON: %v\n%s", method, path, status, err, body)
		return
	}
	if err := schema.Validate(instance); err != nil {
		s.t.Errorf("%s %s %d: body does not match its schema: %v\nbody: %s", method, path, status, err, body)
	}
}

// resolve follows a {$ref: "#/components/responses/X"} object.
func (s *spec) resolve(v any) map[string]any {
	m, _ := v.(map[string]any)
	ref, ok := m["$ref"].(string)
	if !ok {
		return m
	}
	node := any(s.doc)
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		node, _ = node.(map[string]any)[part]
	}
	resolved, _ := node.(map[string]any)
	return resolved
}

func (s *spec) schemaFor(method, path string, status int, mediaType string, _ map[string]any) *jsonschema.Schema {
	key := fmt.Sprintf("%s %s %d", method, path, status)
	if c, ok := s.compiled[key]; ok {
		return c
	}
	op := s.operation(method, path)
	responses, _ := op["responses"].(map[string]any)
	pointer := "#/paths/" + escape(path) + "/" + strings.ToLower(method) + "/responses/" + strconv.Itoa(status)
	if _, isRef := responses[strconv.Itoa(status)].(map[string]any)["$ref"]; isRef {
		ref := responses[strconv.Itoa(status)].(map[string]any)["$ref"].(string)
		pointer = ref
	}
	pointer += "/content/" + escape(mediaType) + "/schema"
	compiled, err := s.compiler.Compile(specURL + pointer)
	if err != nil {
		s.t.Fatalf("compile schema of %s: %v", key, err)
	}
	s.compiled[key] = compiled
	return compiled
}

func (s *spec) componentSchema(name string) *jsonschema.Schema {
	s.t.Helper()
	compiled, err := s.compiler.Compile(specURL + "#/components/schemas/" + name)
	if err != nil {
		s.t.Fatalf("compile component %s: %v", name, err)
	}
	return compiled
}

// escape makes a path or media type usable as one JSON pointer segment in a URI.
func escape(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	s = strings.ReplaceAll(s, "{", "%7B")
	return strings.ReplaceAll(s, "}", "%7D")
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// routesOf lists "METHOD /path" for every route of the router.
func routesOf(t *testing.T, h http.Handler) map[string]bool {
	t.Helper()
	mux, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("router is %T, not a chi router", h)
	}
	out := map[string]bool{}
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// chi writes a wildcard as /*; OpenAPI as a path parameter.
		out[method+" "+strings.Replace(route, "/*", "/{path}", 1)] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func TestOpenAPIAndRouterListTheSameRoutes(t *testing.T) {
	s := loadSpec(t)
	h, _ := newTestServer(t, fakePinger{})
	routes := routesOf(t, h)
	documented := s.operations()
	if missing := diff(routes, documented); len(missing) > 0 {
		t.Errorf("routes missing from openapi.yaml: %v", missing)
	}
	if extra := diff(documented, routes); len(extra) > 0 {
		t.Errorf("openapi.yaml documents routes the router does not have: %v", extra)
	}
	if len(routes) < 9 {
		t.Errorf("expected at least the 9 known routes, found %v", routes)
	}
}

func TestOpenAPIIsWellFormed(t *testing.T) {
	s := loadSpec(t)
	if v, _ := s.doc["openapi"].(string); !strings.HasPrefix(v, "3.1.") {
		t.Errorf("openapi = %q, want 3.1.x", v)
	}
	info, _ := s.doc["info"].(map[string]any)
	if info["title"] == nil || info["version"] == nil {
		t.Errorf("info needs a title and a version: %v", info)
	}
	seen := map[string]string{}
	for op := range s.operations() {
		method, path, _ := strings.Cut(op, " ")
		node := s.operation(method, path)
		id, _ := node["operationId"].(string)
		if id == "" {
			t.Errorf("%s has no operationId", op)
		} else if other, dup := seen[id]; dup {
			t.Errorf("operationId %q is used by %s and %s", id, other, op)
		}
		seen[id] = op
		responses, _ := node["responses"].(map[string]any)
		if len(responses) == 0 {
			t.Errorf("%s documents no responses", op)
		}
		for status, r := range responses {
			if s.resolve(r)["description"] == nil {
				t.Errorf("%s %s has no description", op, status)
			}
		}
	}
}

func TestEveryDocumentedSchemaCompiles(t *testing.T) {
	s := loadSpec(t)
	schemasMap, _ := s.doc["components"].(map[string]any)["schemas"].(map[string]any)
	if len(schemasMap) < 8 {
		t.Fatalf("only %d component schemas", len(schemasMap))
	}
	for name := range schemasMap {
		s.componentSchema(name)
	}
}

func TestSpecCatchesAResponseThatBreaksItsSchema(t *testing.T) {
	// A guard for the guard: the checker must fail on a bad body.
	s := loadSpec(t)
	schema := s.componentSchema("Task")
	bad := `{"id":"not-a-uuid","user_id":"u","workflow_id":"","source":"fax","status":"done","payload":[],"result":1,"error":2,"created_at":"yesterday","started_at":null,"finished_at":null,"queue_seconds":-1,"execution_seconds":null,"extra":true}`
	instance, _ := jsonschema.UnmarshalJSON(strings.NewReader(bad))
	if err := schema.Validate(instance); err == nil {
		t.Fatal("the Task schema accepted an obviously broken task")
	}
}

package message

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"k8s-lab/shared/yamlschema"
)

// Operation is one thing an app can be asked, with the JSON Schema (draft
// 2020-12) of the payload of its request and of its response.
type Operation struct {
	Name     string
	Request  json.RawMessage
	Response json.RawMessage
}

// Registry holds the operations an app knows. Register at start up; looking up
// and validating are safe to do from many goroutines.
type Registry struct {
	mu         sync.RWMutex
	operations map[string]compiledOperation
}

type compiledOperation struct {
	request  *jsonschema.Schema
	response *jsonschema.Schema
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{operations: map[string]compiledOperation{}}
}

// Register compiles the schemas of an operation and adds it. It fails when the
// name is not a valid operation name or is already registered, or when a schema
// does not compile.
func (r *Registry) Register(op Operation) error {
	if !operationRe.MatchString(op.Name) {
		return fmt.Errorf("register operation %q: the name must match %s", op.Name, operationRe)
	}
	request, err := compileSchema(op.Name, "request", op.Request)
	if err != nil {
		return err
	}
	response, err := compileSchema(op.Name, "response", op.Response)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.operations[op.Name]; ok {
		return fmt.Errorf("register operation %q: already registered", op.Name)
	}
	r.operations[op.Name] = compiledOperation{request: request, response: response}
	return nil
}

func compileSchema(operation, side string, schemaJSON json.RawMessage) (*jsonschema.Schema, error) {
	url := "operation/" + operation + "/" + side + ".json"
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("register operation %q: parse %s schema: %w", operation, side, err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("register operation %q: add %s schema: %w", operation, side, err)
	}
	compiled, err := compiler.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("register operation %q: compile %s schema: %w", operation, side, err)
	}
	return compiled, nil
}

// Has reports whether the operation is registered.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.operations[name]
	return ok
}

// Names returns the registered operation names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.operations))
	for name := range r.operations {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateRequestPayload checks the payload of a request for the operation. It
// returns unknown_operation when the operation is not registered and
// invalid_payload, naming the path and the reason, when the payload breaks the
// schema. The errors are *Error values.
func (r *Registry) ValidateRequestPayload(op string, payload json.RawMessage) error {
	return r.validatePayload(op, payload, func(c compiledOperation) *jsonschema.Schema { return c.request })
}

// ValidateResponsePayload is ValidateRequestPayload for the payload of a response.
func (r *Registry) ValidateResponsePayload(op string, payload json.RawMessage) error {
	return r.validatePayload(op, payload, func(c compiledOperation) *jsonschema.Schema { return c.response })
}

func (r *Registry) validatePayload(op string, payload json.RawMessage, pick func(compiledOperation) *jsonschema.Schema) error {
	r.mu.RLock()
	compiled, ok := r.operations[op]
	r.mu.RUnlock()
	if !ok {
		return newError(CodeUnknownOperation, "the operation is not known to the receiver")
	}
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
	if err != nil {
		return newError(CodeInvalidPayload, "payload is not valid JSON")
	}
	err = pick(compiled).Validate(instance)
	if err == nil {
		return nil
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return newError(CodeInternal, "payload could not be checked")
	}
	return newError(CodeInvalidPayload, describe(verr))
}

// describe words the violations of a payload with the path of each.
func describe(verr *jsonschema.ValidationError) string {
	var problems []string
	for _, leaf := range yamlschema.Leaves(verr) {
		path := yamlschema.FormatPath(leaf.InstanceLocation)
		problems = append(problems, path+": "+leaf.ErrorKind.LocalizedString(yamlschema.Printer))
	}
	sort.Strings(problems)
	return "payload is not valid: " + strings.Join(problems, "; ")
}

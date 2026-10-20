package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"k8s-lab/shared/yamlschema"
)

// Status is where a run is in its life.
type Status string

const (
	StatusRunning   Status = "running"
	StatusWaiting   Status = "waiting"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Run is one execution of a workflow. It is plain data: it marshals to JSON
// and back, so a waiting run can be stored and resumed by another process.
//
// State holds JSON values decoded with json.Number for numbers (see
// DecodeObject), so integers survive a round trip unchanged.
type Run struct {
	RunID      string         `json:"run_id"`
	WorkflowID string         `json:"workflow_id"`
	Status     Status         `json:"status"`
	StateName  string         `json:"state_name"`
	State      map[string]any `json:"state"`
	Visits     map[string]int `json:"visits"`
	Steps      int            `json:"steps"`
	// Tokens is the input plus output tokens the run has used so far.
	Tokens int `json:"tokens"`
	// Observation is the result of the last action, which the next step sees:
	// a tool result, an error, or the user's message after a resume.
	Observation any `json:"observation,omitempty"`
	// ErrorEdgeUsed is true once the run has taken a state's on_error edge;
	// it may do so only once.
	ErrorEdgeUsed bool `json:"error_edge_used,omitempty"`
	// Failure says why a failed run failed.
	Failure *Failure `json:"failure,omitempty"`
	// Language is the language code the prompts are specialised for. A string
	// "language" key in State, if the model has recorded one, takes precedence.
	Language string `json:"language,omitempty"`
}

// Failure is why a run failed.
type Failure struct {
	Reason  FailReason `json:"reason"`
	Message string     `json:"message"`
}

// FailReason names the way a run can fail.
type FailReason string

const (
	// FailRetriesExhausted: the model kept sending replies the engine refused.
	FailRetriesExhausted FailReason = "retries_exhausted"
	// FailMaxSteps: the run used its step budget.
	FailMaxSteps FailReason = "max_steps"
	// FailMaxTokens: the run used its token budget.
	FailMaxTokens FailReason = "max_tokens"
	// FailMaxVisits: the run entered one state too many times.
	FailMaxVisits FailReason = "max_visits"
	// FailCancelled: the context was cancelled.
	FailCancelled FailReason = "cancelled"
	// FailLLM: the model call itself failed.
	FailLLM FailReason = "llm_error"
	// FailIncompleteState: the run reached an end state without the state its
	// schema requires.
	FailIncompleteState FailReason = "incomplete_state"
)

// RestoreRun decodes a run snapshot written by json.Marshal, keeping numbers
// exact. Use it instead of json.Unmarshal, which would turn integers in the
// run state into floats.
func RestoreRun(data []byte) (*Run, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var r Run
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("restore run: %w", err)
	}
	if r.State == nil {
		r.State = map[string]any{}
	}
	if r.Visits == nil {
		r.Visits = map[string]int{}
	}
	return &r, nil
}

// language returns the language code to use for prompt snippets.
func (r *Run) language() string {
	if l, ok := r.State["language"].(string); ok && l != "" {
		return l
	}
	return r.Language
}

// NewRun returns a run positioned at the workflow's start state, which counts
// as visited once. initial is copied; nil means an empty state.
func NewRun(wf *Workflow, runID string, initial map[string]any) *Run {
	state := deepCopyObject(initial)
	if state == nil {
		state = map[string]any{}
	}
	return &Run{
		RunID:      runID,
		WorkflowID: wf.ID,
		Status:     StatusRunning,
		StateName:  wf.Start,
		State:      state,
		Visits:     map[string]int{wf.Start: 1},
	}
}

// DecodeObject parses a JSON object, keeping numbers as json.Number so the
// result can be validated and merged without losing integer precision.
func DecodeObject(data []byte) (map[string]any, error) {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object, got %s", describeKind(v))
	}
	return obj, nil
}

func describeKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case []any:
		return "an array"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// MergePatch returns state with patch merged in, leaving both arguments
// untouched. Objects merge recursively, a null value deletes its key, and any
// other value, arrays included, replaces what was there.
func MergePatch(state, patch map[string]any) map[string]any {
	result := deepCopyObject(state)
	if result == nil {
		result = map[string]any{}
	}
	mergeInto(result, patch)
	return result
}

func mergeInto(target, patch map[string]any) {
	for key, value := range patch {
		switch v := value.(type) {
		case nil:
			delete(target, key)
		case map[string]any:
			existing, ok := target[key].(map[string]any)
			if !ok {
				existing = map[string]any{}
			}
			mergeInto(existing, v)
			target[key] = existing
		default:
			target[key] = deepCopyValue(v)
		}
	}
}

func deepCopyObject(obj map[string]any) map[string]any {
	if obj == nil {
		return nil
	}
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyObject(t)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return v
	}
}

// StateError lists why a run state was rejected, in words meant to be shown
// to the model so it can correct itself.
type StateError struct {
	Problems []string
}

func (e *StateError) Error() string {
	return "invalid state: " + strings.Join(e.Problems, "; ")
}

// StateValidator checks a run state against a workflow's state_schema.
type StateValidator struct {
	schema *jsonschema.Schema
}

// NewStateValidator compiles a state_schema.
func NewStateValidator(schema map[string]any) (*StateValidator, error) {
	if schema == nil {
		return nil, errors.New("state_schema is missing")
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode state_schema: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("parse state_schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	const url = "state_schema.json"
	if err := compiler.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("state_schema is not a valid JSON Schema: %w", err)
	}
	compiled, err := compiler.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("state_schema is not a valid JSON Schema: %w", err)
	}
	return &StateValidator{schema: compiled}, nil
}

// Validate checks state against the schema and returns a *StateError when it
// does not fit. A state still being filled in is not expected to have every
// required key, so unless complete is true, missing required keys are not
// reported; type and shape problems always are. Pass complete when the run is
// about to finish.
func (v *StateValidator) Validate(state map[string]any, complete bool) error {
	instance := any(state)
	if state == nil {
		instance = map[string]any{}
	}
	err := v.schema.Validate(instance)
	if err == nil {
		return nil
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return fmt.Errorf("validate state: %w", err)
	}

	var problems []string
	for _, leaf := range yamlschema.Leaves(verr) {
		if _, isRequired := leaf.ErrorKind.(*kind.Required); isRequired && !complete {
			continue
		}
		problems = append(problems, describeLeaf(leaf))
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return &StateError{Problems: problems}
}

func describeLeaf(leaf *jsonschema.ValidationError) string {
	return describeLeafAt("state", leaf)
}

// describeLeafAt words one validation failure, naming the location under root.
func describeLeafAt(root string, leaf *jsonschema.ValidationError) string {
	path := root
	if len(leaf.InstanceLocation) > 0 {
		path += "." + strings.Join(leaf.InstanceLocation, ".")
	}
	switch k := leaf.ErrorKind.(type) {
	case *kind.Required:
		return fmt.Sprintf("%s is missing required %s", path, quoteAll(k.Missing))
	case *kind.AdditionalProperties:
		return fmt.Sprintf("%s has unknown %s", path, quoteAll(k.Properties))
	default:
		return fmt.Sprintf("%s: %s", path, leaf.ErrorKind.LocalizedString(yamlschema.Printer))
	}
}

func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// ApplyPatch merges patch into the run's state and commits the result only if
// it passes validation. On a *StateError the run is left exactly as it was.
func (r *Run) ApplyPatch(v *StateValidator, patch map[string]any, complete bool) error {
	merged := MergePatch(r.State, patch)
	if err := v.Validate(merged, complete); err != nil {
		return err
	}
	r.State = merged
	return nil
}

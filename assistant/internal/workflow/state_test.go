package workflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func obj(t *testing.T, s string) map[string]any {
	t.Helper()
	o, err := DecodeObject([]byte(s))
	if err != nil {
		t.Fatalf("DecodeObject(%s): %v", s, err)
	}
	return o
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMergePatch(t *testing.T) {
	tests := []struct {
		name, state, patch, want string
	}{
		{"adds a key", `{"a":1}`, `{"b":2}`, `{"a":1,"b":2}`},
		{"replaces a scalar", `{"a":1}`, `{"a":2}`, `{"a":2}`},
		{"null deletes a key", `{"a":1,"b":2}`, `{"a":null}`, `{"b":2}`},
		{"null for a missing key is a no-op", `{"a":1}`, `{"zzz":null}`, `{"a":1}`},
		{"merges objects recursively", `{"o":{"x":1,"y":2}}`, `{"o":{"y":3,"z":4}}`, `{"o":{"x":1,"y":3,"z":4}}`},
		{"null deletes a nested key", `{"o":{"x":1,"y":2}}`, `{"o":{"x":null}}`, `{"o":{"y":2}}`},
		{"replaces arrays whole", `{"a":[1,2,3]}`, `{"a":[9]}`, `{"a":[9]}`},
		{"empty array replaces", `{"a":[1]}`, `{"a":[]}`, `{"a":[]}`},
		{"object replaces a scalar", `{"a":1}`, `{"a":{"b":2}}`, `{"a":{"b":2}}`},
		{"scalar replaces an object", `{"a":{"b":2}}`, `{"a":5}`, `{"a":5}`},
		{"nulls inside a new object are dropped", `{}`, `{"a":{"b":null,"c":1}}`, `{"a":{"c":1}}`},
		{"empty patch changes nothing", `{"a":1}`, `{}`, `{"a":1}`},
		{"patch on empty state", `{}`, `{"a":1}`, `{"a":1}`},
		{"deleting then re-adding in one patch is a replace", `{"a":1}`, `{"a":null,"b":1}`, `{"b":1}`},
		{"keeps large integers exact", `{}`, `{"n":12345678901234567890}`, `{"n":12345678901234567890}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := asJSON(t, MergePatch(obj(t, tc.state), obj(t, tc.patch)))
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMergePatchDoesNotMutateInputs(t *testing.T) {
	state := obj(t, `{"o":{"x":1},"list":[1,2]}`)
	patch := obj(t, `{"o":{"y":2},"list":[3],"gone":null}`)
	stateBefore, patchBefore := asJSON(t, state), asJSON(t, patch)

	merged := MergePatch(state, patch)
	merged["o"].(map[string]any)["x"] = "changed"
	merged["list"].([]any)[0] = "changed"

	if asJSON(t, state) != stateBefore {
		t.Errorf("state was mutated: %s", asJSON(t, state))
	}
	if asJSON(t, patch) != patchBefore {
		t.Errorf("patch was mutated: %s", asJSON(t, patch))
	}
}

func TestMergePatchResultDoesNotShareStructureWithPatch(t *testing.T) {
	patch := obj(t, `{"list":[{"k":1}]}`)
	merged := MergePatch(map[string]any{}, patch)
	merged["list"].([]any)[0].(map[string]any)["k"] = "changed"
	if asJSON(t, patch) != `{"list":[{"k":1}]}` {
		t.Errorf("patch shares structure with the merged state: %s", asJSON(t, patch))
	}
}

func TestMergePatchNilState(t *testing.T) {
	got := MergePatch(nil, obj(t, `{"a":1}`))
	if asJSON(t, got) != `{"a":1}` {
		t.Errorf("got %s", asJSON(t, got))
	}
}

func TestDecodeObjectRejectsNonObjects(t *testing.T) {
	for in, want := range map[string]string{
		`[1]`:      "an array",
		`"text"`:   "a string",
		`3`:        "a number",
		`null`:     "null",
		`true`:     "a boolean",
		`{"a":`:    "",
		``:         "",
		`{"a":1}x`: "",
	} {
		_, err := DecodeObject([]byte(in))
		if err == nil {
			t.Errorf("DecodeObject(%q) should fail", in)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("DecodeObject(%q) error = %q, want mention of %q", in, err, want)
		}
	}
}

const learnSchema = `{
  "type": "object",
  "properties": {
    "goal": {"type": "string"},
    "level": {"enum": ["beginner", "intermediate", "advanced"]},
    "gaps": {"type": "array", "items": {"type": "string"}},
    "lessons_done": {"type": "integer", "minimum": 0}
  },
  "required": ["goal"],
  "additionalProperties": false
}`

func validator(t *testing.T, schema string) *StateValidator {
	t.Helper()
	v, err := NewStateValidator(obj(t, schema))
	if err != nil {
		t.Fatalf("NewStateValidator: %v", err)
	}
	return v
}

func TestStateValidatorAcceptsValidState(t *testing.T) {
	v := validator(t, learnSchema)
	for _, complete := range []bool{false, true} {
		if err := v.Validate(obj(t, `{"goal":"Arabic","level":"beginner","gaps":["verbs"],"lessons_done":3}`), complete); err != nil {
			t.Errorf("complete=%v: %v", complete, err)
		}
	}
}

func TestStateValidatorChecksTypesAlways(t *testing.T) {
	v := validator(t, learnSchema)
	tests := map[string]string{
		`{"goal":5}`:                   "state.goal",
		`{"level":"expert"}`:           "state.level",
		`{"gaps":["a",2]}`:             "state.gaps.1",
		`{"lessons_done":-1}`:          "state.lessons_done",
		`{"lessons_done":1.5}`:         "state.lessons_done",
		`{"goal":"x","surprise":true}`: `unknown "surprise"`,
	}
	for in, want := range tests {
		for _, complete := range []bool{false, true} {
			err := v.Validate(obj(t, in), complete)
			var serr *StateError
			if !errors.As(err, &serr) {
				t.Errorf("%s complete=%v: want *StateError, got %v", in, complete, err)
				continue
			}
			if !strings.Contains(serr.Error(), want) {
				t.Errorf("%s: error %q does not mention %q", in, serr, want)
			}
		}
	}
}

func TestStateValidatorRequiredOnlyWhenComplete(t *testing.T) {
	v := validator(t, learnSchema)
	if err := v.Validate(obj(t, `{"level":"beginner"}`), false); err != nil {
		t.Errorf("incomplete state should not need required keys: %v", err)
	}
	err := v.Validate(obj(t, `{"level":"beginner"}`), true)
	var serr *StateError
	if !errors.As(err, &serr) || !strings.Contains(serr.Error(), `missing required "goal"`) {
		t.Errorf("complete state must report the missing key, got %v", err)
	}
}

func TestStateValidatorNestedRequiredOnlyWhenComplete(t *testing.T) {
	v := validator(t, `{"type":"object","properties":{"p":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}}}`)
	if err := v.Validate(obj(t, `{"p":{}}`), false); err != nil {
		t.Errorf("nested required should wait until complete: %v", err)
	}
	if err := v.Validate(obj(t, `{"p":{}}`), true); err == nil {
		t.Errorf("nested required should be enforced when complete")
	}
}

func TestStateValidatorReportsAllProblemsSorted(t *testing.T) {
	v := validator(t, learnSchema)
	err := v.Validate(obj(t, `{"goal":1,"level":"x","lessons_done":"many"}`), false)
	var serr *StateError
	if !errors.As(err, &serr) || len(serr.Problems) != 3 {
		t.Fatalf("want 3 problems, got %v", err)
	}
	for i := 1; i < len(serr.Problems); i++ {
		if serr.Problems[i] < serr.Problems[i-1] {
			t.Errorf("problems not sorted: %v", serr.Problems)
		}
	}
}

func TestStateValidatorNilStateIsAnEmptyObject(t *testing.T) {
	v := validator(t, learnSchema)
	if err := v.Validate(nil, false); err != nil {
		t.Errorf("nil incomplete state: %v", err)
	}
	if err := v.Validate(nil, true); err == nil {
		t.Errorf("nil complete state should miss the required goal")
	}
}

func TestNewStateValidatorRejectsBadSchemas(t *testing.T) {
	if _, err := NewStateValidator(nil); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("nil schema: %v", err)
	}
	if _, err := NewStateValidator(obj(t, `{"type":"banana"}`)); err == nil || !strings.Contains(err.Error(), "not a valid JSON Schema") {
		t.Errorf("bad type: %v", err)
	}
	if _, err := NewStateValidator(map[string]any{"bad": func() {}}); err == nil {
		t.Errorf("unencodable schema should fail")
	}
}

func testWorkflow() *Workflow {
	return &Workflow{ID: "learn", Start: "intake", States: map[string]State{"intake": {Instructions: "x"}}}
}

func TestNewRun(t *testing.T) {
	initial := obj(t, `{"goal":"Arabic"}`)
	run := NewRun(testWorkflow(), "run-1", initial)
	if run.RunID != "run-1" || run.WorkflowID != "learn" || run.Status != StatusRunning || run.StateName != "intake" {
		t.Errorf("unexpected run: %+v", run)
	}
	if run.Visits["intake"] != 1 || run.Steps != 0 {
		t.Errorf("start state should count as visited once: %+v", run)
	}
	initial["goal"] = "changed"
	if run.State["goal"] != "Arabic" {
		t.Errorf("run shares its state with the caller's map")
	}
	if got := NewRun(testWorkflow(), "r", nil).State; got == nil || len(got) != 0 {
		t.Errorf("nil initial state should become an empty object, got %v", got)
	}
}

func TestApplyPatchCommits(t *testing.T) {
	v := validator(t, learnSchema)
	run := NewRun(testWorkflow(), "r", nil)
	if err := run.ApplyPatch(v, obj(t, `{"goal":"Arabic","level":"beginner"}`), false); err != nil {
		t.Fatal(err)
	}
	if err := run.ApplyPatch(v, obj(t, `{"level":null,"lessons_done":1}`), false); err != nil {
		t.Fatal(err)
	}
	if got := asJSON(t, run.State); got != `{"goal":"Arabic","lessons_done":1}` {
		t.Errorf("state = %s", got)
	}
}

func TestApplyPatchRollsBackOnInvalidResult(t *testing.T) {
	v := validator(t, learnSchema)
	run := NewRun(testWorkflow(), "r", obj(t, `{"goal":"Arabic","lessons_done":2}`))
	before := asJSON(t, run.State)

	err := run.ApplyPatch(v, obj(t, `{"lessons_done":"three","gaps":["ok"]}`), false)
	var serr *StateError
	if !errors.As(err, &serr) {
		t.Fatalf("want *StateError, got %v", err)
	}
	if asJSON(t, run.State) != before {
		t.Errorf("state changed on a rejected patch: %s", asJSON(t, run.State))
	}
}

func TestApplyPatchCompleteEnforcesRequired(t *testing.T) {
	v := validator(t, learnSchema)
	run := NewRun(testWorkflow(), "r", obj(t, `{"level":"beginner"}`))
	if err := run.ApplyPatch(v, obj(t, `{"lessons_done":1}`), true); err == nil {
		t.Fatalf("completing without the required goal should be rejected")
	}
	if _, has := run.State["lessons_done"]; has {
		t.Errorf("rejected patch leaked into state")
	}
	if err := run.ApplyPatch(v, obj(t, `{"goal":"x"}`), true); err != nil {
		t.Errorf("completing with the goal: %v", err)
	}
}

func TestApplyPatchOnADeletedKey(t *testing.T) {
	v := validator(t, learnSchema)
	run := NewRun(testWorkflow(), "r", obj(t, `{"goal":"x","level":"beginner"}`))
	_ = run.ApplyPatch(v, obj(t, `{"level":null}`), false)
	if err := run.ApplyPatch(v, obj(t, `{"level":"advanced"}`), false); err != nil {
		t.Fatal(err)
	}
	if run.State["level"] != "advanced" {
		t.Errorf("re-adding a deleted key failed: %v", run.State)
	}
	if !reflect.DeepEqual(run.Visits, map[string]int{"intake": 1}) {
		t.Errorf("patches must not change visits: %v", run.Visits)
	}
}

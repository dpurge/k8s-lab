package workflow_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"

	"assistant/internal/workflow"
)

// The evaluation cases are authored in prompt-eval (assistant-step/cases.yaml)
// and synced here. These tests keep them honest: each case must be exactly
// the request the engine would build for that step, its reply schema must be
// the one the engine builds, and its example reply must be accepted by the
// engine. A change to the workflow or the engine that breaks a case fails here
// instead of silently making the evaluation measure the wrong thing.
//
// To rewrite the schema files after the contract changed, run
//   WRITE_EVAL_SCHEMAS=<prompt-eval>/assistant-step/schemas go test ./internal/workflow -run TestPromptEvalCaseSchemas
// and then sync them back with npm run sync-assistant in prompt-eval.

const evalDir = "testdata/prompt-eval"

type evalCase struct {
	ID      string            `yaml:"id"`
	Select  map[string]string `yaml:"select"`
	Input   map[string]any    `yaml:"input"`
	Expect  []any             `yaml:"expect"`
	Example string            `yaml:"example"`
}

func loadEvalCases(t *testing.T) []evalCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(evalDir, "cases.yaml"))
	if err != nil {
		t.Fatalf("%v\n(sync it from prompt-eval: npm run sync-assistant)", err)
	}
	var doc struct {
		Cases []evalCase `yaml:"cases"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) < 8 {
		t.Fatalf("only %d cases", len(doc.Cases))
	}
	return doc.Cases
}

func (c evalCase) stateName(t *testing.T) string {
	t.Helper()
	state, ok := c.Input["state"].(map[string]any)
	if !ok {
		t.Fatalf("case %s has no input.state", c.ID)
	}
	name, ok := state["name"].(string)
	if !ok {
		t.Fatalf("case %s has no input.state.name", c.ID)
	}
	return name
}

// normalised returns v as the engine would hold it: through JSON, with
// numbers as json.Number, so values compare equal however they were written.
func normalised(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	dec := json.NewDecoder(bytesReader(b))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// runFor builds the run a case describes: in its state, with its run state.
func runFor(t *testing.T, wf *workflow.Workflow, c evalCase) *workflow.Run {
	t.Helper()
	run := workflow.NewRun(wf, "eval", nil)
	run.StateName = c.stateName(t)
	if rs, ok := c.Input["run_state"].(map[string]any); ok {
		state, ok := normalised(t, rs).(map[string]any)
		if !ok {
			t.Fatalf("case %s: run_state is not an object", c.ID)
		}
		run.State = state
	}
	run.Language = c.Select["language"]
	return run
}

func retryOf(t *testing.T, c evalCase) *workflow.Retry {
	t.Helper()
	raw, ok := c.Input["retry"].(map[string]any)
	if !ok {
		return nil
	}
	reply, _ := raw["reply"].(string)
	msg, _ := raw["error"].(string)
	return &workflow.Retry{Reply: reply, Error: msg}
}

func TestPromptEvalCaseInputsAreWhatTheEngineBuilds(t *testing.T) {
	wf := loadLearnTopic(t)
	_, reg := learnToolSet(t)
	prompts, err := workflow.DefaultPrompts()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range loadEvalCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			run := runFor(t, wf, c)
			var observation any
			if obs, ok := c.Input["observation"]; ok {
				observation = normalised(t, obs)
			}
			req, err := workflow.BuildRequest(prompts, wf, run, reg, observation, retryOf(t, c))
			if err != nil {
				t.Fatal(err)
			}
			built, err := workflow.DecodeObject([]byte(req.Input))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := normalised(t, built), normalised(t, c.Input); !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.MarshalIndent(got, "", " ")
				wantJSON, _ := json.MarshalIndent(want, "", " ")
				t.Errorf("the case input is not what the engine builds.\nengine:\n%s\ncase:\n%s", gotJSON, wantJSON)
			}
		})
	}
}

func TestPromptEvalCasesSelectWhatTheEngineWould(t *testing.T) {
	wf := loadLearnTopic(t)
	prompts, err := workflow.DefaultPrompts()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range loadEvalCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			state := wf.States[c.stateName(t)]
			if c.Select["output"] != state.Output {
				t.Errorf("select.output = %q but state %s declares output %q", c.Select["output"], c.stateName(t), state.Output)
			}
			if lang := c.Select["language"]; lang != "" {
				found := false
				for _, l := range prompts.Languages() {
					found = found || l == lang
				}
				if !found {
					t.Errorf("language %q has no snippet in the engine's prompts", lang)
				}
			}
		})
	}
}

func TestPromptEvalCaseSchemas(t *testing.T) {
	wf := loadLearnTopic(t)
	_, reg := learnToolSet(t)
	states := map[string]bool{}
	for _, c := range loadEvalCases(t) {
		states[c.stateName(t)] = true
	}
	writeTo := os.Getenv("WRITE_EVAL_SCHEMAS")
	for state := range states {
		t.Run(state, func(t *testing.T) {
			schema, err := workflow.ReplySchema(wf, state, reg)
			if err != nil {
				t.Fatal(err)
			}
			if writeTo != "" {
				pretty, _ := json.MarshalIndent(schema, "", "  ")
				if err := os.WriteFile(filepath.Join(writeTo, state+".json"), append(pretty, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			data, err := os.ReadFile(filepath.Join(evalDir, "schemas", state+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var synced any
			dec := json.NewDecoder(bytesReader(data))
			dec.UseNumber()
			if err := dec.Decode(&synced); err != nil {
				t.Fatal(err)
			}
			if got := normalised(t, schema); !reflect.DeepEqual(got, synced) {
				t.Errorf("schemas/%s.json is not the reply schema the engine builds for state %s; regenerate it (see the comment at the top of this file)", state, state)
			}
		})
	}
}

func TestPromptEvalExamplesAreAcceptedByTheEngine(t *testing.T) {
	wf := loadLearnTopic(t)
	_, reg := learnToolSet(t)
	validator, err := workflow.NewStateValidator(wf.StateSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range loadEvalCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			state := c.stateName(t)
			reply, err := workflow.ParseReply(wf, state, reg, c.Example)
			if err != nil {
				t.Fatalf("the engine refuses the example reply: %v", err)
			}
			run := runFor(t, wf, c)
			if err := validator.Validate(run.State, false); err != nil {
				t.Fatalf("the case's run_state is not a valid run state: %v", err)
			}
			complete := reply.Action.Type == workflow.ActionGoto && wf.States[reply.Action.State].End
			if err := run.ApplyPatch(validator, reply.StatePatch, complete); err != nil {
				t.Errorf("the engine would refuse the example's state_patch: %v", err)
			}
		})
	}
}

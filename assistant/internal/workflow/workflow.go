// Package workflow is the assistant's state-machine engine: workflows are
// YAML files, a run walks one state at a time, and an LLM proposes each
// step, which the runtime validates before committing it.
package workflow

import (
	"go.yaml.in/yaml/v3"

	"k8s-lab/shared/yamlschema"
)

// Workflow is a loaded workflow definition.
type Workflow struct {
	ID          string         `yaml:"id"`
	Version     int            `yaml:"version"`
	Description string         `yaml:"description"`
	StateSchema map[string]any `yaml:"state_schema"`
	Start       string         `yaml:"start"`
	Limits      Limits         `yaml:"limits"`
	// ModelTags are what every step needs from a model unless its state
	// lists its own. Empty means any model.
	ModelTags []string `yaml:"model_tags"`
	// UI says the workflow may show interfaces (A2UI) in its ask_user payloads.
	// The prompt then carries a guide to the catalog, and an interface in a reply
	// is checked before the reply is accepted, so a model that writes an invalid
	// one is told what is wrong and tries again within max_retries.
	UI *UIConfig `yaml:"ui"`
	// Intents say what the workflow handles. A workflow that has them is offered
	// to the orchestrator, which routes a user's message to the workflow whose
	// intents fit it; one without is never offered.
	Intents *Intents         `yaml:"intents"`
	States  map[string]State `yaml:"states"`

	// root is the parsed YAML document, kept so findings can name a line.
	// It is nil for a Workflow built in code.
	root *yaml.Node
}

// line returns the source line of the node at the key path, or 0 when the
// workflow was not loaded from a file.
func (w *Workflow) line(segments ...string) int {
	if w.root == nil {
		return 0
	}
	return yamlschema.LineOf(w.root, segments)
}

// UIConfig declares what interfaces a workflow may show.
type UIConfig struct {
	// Catalogs are the A2UI catalogs the model may use; "basic" is the only one.
	Catalogs []string `yaml:"catalogs"`
}

// Intents describe what a workflow is for, to the model that chooses between
// workflows.
type Intents struct {
	// Description is one or two sentences: what requests this workflow handles.
	Description string `yaml:"description"`
	// Examples are requests it handles, in a user's words.
	Examples []string `yaml:"examples"`
}

// Limits are the guards that stop a runaway run.
type Limits struct {
	MaxSteps   int `yaml:"max_steps"`
	MaxTokens  int `yaml:"max_tokens"`
	MaxRetries int `yaml:"max_retries"`
	MaxVisits  int `yaml:"max_visits"`
}

// State is one node of the machine.
type State struct {
	Instructions string   `yaml:"instructions"`
	Tools        []string `yaml:"tools"`
	Next         []string `yaml:"next"`
	End          bool     `yaml:"end"`
	OnError      string   `yaml:"on_error"`
	Output       string   `yaml:"output"`
	// Delegate lets the model hand a request to another workflow (a subagent)
	// from this state, with the delegate action. The workflows it may name are
	// the ones that declare intents.
	Delegate bool `yaml:"delegate"`
	// ModelTags replace the workflow's for steps in this state. A present but
	// empty list means any model; nil means inherit.
	ModelTags []string `yaml:"model_tags"`
}

// TagsFor returns the model tags a step in the named state needs: the
// state's own list when it has one, else the workflow's.
func (w *Workflow) TagsFor(stateName string) []string {
	if st, ok := w.States[stateName]; ok && st.ModelTags != nil {
		return st.ModelTags
	}
	return w.ModelTags
}

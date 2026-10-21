package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"k8s-lab/shared/yamlschema"
)

// ActionType is what the model asks the runtime to do next.
type ActionType string

const (
	ActionTool    ActionType = "tool"
	ActionGoto    ActionType = "goto"
	ActionAskUser ActionType = "ask_user"
	// ActionDelegate hands a request to another workflow.
	ActionDelegate ActionType = "delegate"
)

// Action is the parsed action of a reply. Only the fields of its Type are set.
type Action struct {
	Type    ActionType
	Tool    string         // ActionTool
	Args    map[string]any // ActionTool
	State   string         // ActionGoto
	Payload map[string]any // ActionAskUser

	Workflow string // ActionDelegate: the workflow to hand the request to
	Message  any    // ActionDelegate: the request, the first observation of the child
	Reason   string // ActionDelegate: why, in a few words, shown to the user
}

// Reply is a validated model reply: a state patch plus one action.
type Reply struct {
	StatePatch map[string]any
	Action     Action
}

// ReplyError says why a reply was refused, in words fit to show the model.
type ReplyError struct {
	Message string
}

func (e *ReplyError) Error() string { return e.Message }

func refuse(format string, args ...any) error {
	return &ReplyError{Message: fmt.Sprintf(format, args...)}
}

// ParseReply parses and validates the model's reply for the named state. It
// returns a *ReplyError for anything the model can correct: text that is not
// a JSON object, wrong keys or types, an unknown action, a tool the state
// does not list, arguments that break the tool's schema, or a target state
// the state does not list. A reply wrapped in a markdown code fence is
// accepted, since models often add one.
func ParseReply(wf *Workflow, stateName string, tools ToolLookup, text string) (*Reply, error) {
	state, ok := wf.States[stateName]
	if !ok {
		return nil, fmt.Errorf("unknown state %q in workflow %q", stateName, wf.ID)
	}

	body, err := DecodeObject([]byte(stripCodeFence(text)))
	if err != nil {
		return nil, refuse("reply is not a valid JSON object: %v", err)
	}
	if err := checkKeys(body, []string{"state_patch", "action"}, []string{"state_patch", "action"}, "reply"); err != nil {
		return nil, err
	}

	patch, ok := body["state_patch"].(map[string]any)
	if !ok {
		return nil, refuse("state_patch must be a JSON object, got %s", describeKind(body["state_patch"]))
	}
	rawAction, ok := body["action"].(map[string]any)
	if !ok {
		return nil, refuse("action must be a JSON object, got %s", describeKind(body["action"]))
	}

	action, err := parseAction(rawAction, stateName, state, tools)
	if err != nil {
		return nil, err
	}
	return &Reply{StatePatch: patch, Action: *action}, nil
}

func parseAction(raw map[string]any, stateName string, state State, tools ToolLookup) (*Action, error) {
	typ, ok := raw["type"].(string)
	if !ok {
		return nil, refuse(`action needs a "type" string: one of "tool", "goto", "ask_user"%s`, delegateHint(state))
	}
	switch ActionType(typ) {
	case ActionTool:
		return parseToolAction(raw, stateName, state, tools)
	case ActionGoto:
		return parseGotoAction(raw, stateName, state)
	case ActionDelegate:
		return parseDelegateAction(raw, stateName, state)
	case ActionAskUser:
		if err := checkKeys(raw, []string{"type", "payload"}, []string{"type", "payload"}, "action"); err != nil {
			return nil, err
		}
		payload, ok := raw["payload"].(map[string]any)
		if !ok {
			return nil, refuse("ask_user payload must be a JSON object, got %s", describeKind(raw["payload"]))
		}
		return &Action{Type: ActionAskUser, Payload: payload}, nil
	default:
		return nil, refuse("unknown action type %q; use one of \"tool\", \"goto\", \"ask_user\"%s", typ, delegateHint(state))
	}
}

func delegateHint(state State) string {
	if state.Delegate {
		return `, "delegate"`
	}
	return ""
}

func parseDelegateAction(raw map[string]any, stateName string, state State) (*Action, error) {
	if !state.Delegate {
		return nil, refuse("state %q cannot delegate to another workflow", stateName)
	}
	if err := checkKeys(raw, []string{"type", "workflow", "message", "reason"}, []string{"type", "workflow", "message"}, "action"); err != nil {
		return nil, err
	}
	name, ok := raw["workflow"].(string)
	if !ok || name == "" {
		return nil, refuse("delegate workflow must be the id of a workflow, got %s", describeKind(raw["workflow"]))
	}
	reason := ""
	if r, present := raw["reason"]; present {
		if reason, ok = r.(string); !ok {
			return nil, refuse("delegate reason must be a string, got %s", describeKind(r))
		}
	}
	if raw["message"] == nil {
		return nil, refuse("delegate message must not be null: it is the request the other workflow starts from")
	}
	return &Action{Type: ActionDelegate, Workflow: name, Message: raw["message"], Reason: strings.TrimSpace(reason)}, nil
}

func parseToolAction(raw map[string]any, stateName string, state State, tools ToolLookup) (*Action, error) {
	if err := checkKeys(raw, []string{"type", "tool", "args"}, []string{"type", "tool", "args"}, "action"); err != nil {
		return nil, err
	}
	name, ok := raw["tool"].(string)
	if !ok {
		return nil, refuse("action tool must be a string, got %s", describeKind(raw["tool"]))
	}
	if !contains(state.Tools, name) {
		if len(state.Tools) == 0 {
			return nil, refuse("tool %q is not available: state %q has no tools", name, stateName)
		}
		return nil, refuse("tool %q is not available in state %q; available tools: %s", name, stateName, strings.Join(state.Tools, ", "))
	}
	args, ok := raw["args"].(map[string]any)
	if !ok {
		return nil, refuse("args for tool %q must be a JSON object, got %s", name, describeKind(raw["args"]))
	}
	tool, ok := tools.Get(name)
	if !ok {
		return nil, fmt.Errorf("state %q lists tool %q which is not registered", stateName, name)
	}
	if err := validateArgs(tool, args); err != nil {
		return nil, refuse("arguments for tool %q are invalid: %v", name, err)
	}
	return &Action{Type: ActionTool, Tool: name, Args: args}, nil
}

func parseGotoAction(raw map[string]any, stateName string, state State) (*Action, error) {
	if err := checkKeys(raw, []string{"type", "state"}, []string{"type", "state"}, "action"); err != nil {
		return nil, err
	}
	target, ok := raw["state"].(string)
	if !ok {
		return nil, refuse("goto state must be a string, got %s", describeKind(raw["state"]))
	}
	if !contains(state.Next, target) {
		if len(state.Next) == 0 {
			return nil, refuse("cannot go to %q: state %q has no next states", target, stateName)
		}
		return nil, refuse("cannot go to %q from state %q; allowed next states: %s", target, stateName, strings.Join(state.Next, ", "))
	}
	return &Action{Type: ActionGoto, State: target}, nil
}

// checkKeys refuses a JSON object that lacks a required key or has a key
// outside allowed. what names the object in the message.
func checkKeys(obj map[string]any, allowed, required []string, what string) error {
	for _, key := range required {
		if _, ok := obj[key]; !ok {
			return refuse("%s is missing the key %q", what, key)
		}
	}
	var unknown []string
	for key := range obj {
		if !contains(allowed, key) {
			unknown = append(unknown, fmt.Sprintf("%q", key))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return refuse("%s has unknown keys: %s", what, strings.Join(unknown, ", "))
	}
	return nil
}

func validateArgs(tool Tool, args map[string]any) error {
	encoded, err := json.Marshal(tool.ArgsSchema())
	if err != nil {
		return fmt.Errorf("encode args schema: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("parse args schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	const url = "args.json"
	if err := compiler.AddResource(url, doc); err != nil {
		return fmt.Errorf("tool %q has an invalid args schema: %w", tool.Name(), err)
	}
	schema, err := compiler.Compile(url)
	if err != nil {
		return fmt.Errorf("tool %q has an invalid args schema: %w", tool.Name(), err)
	}
	verr := schema.Validate(any(args))
	if verr == nil {
		return nil
	}
	var invalid *jsonschema.ValidationError
	if !errors.As(verr, &invalid) {
		return verr
	}
	var problems []string
	for _, leaf := range yamlschema.Leaves(invalid) {
		problems = append(problems, describeLeafAt("args", leaf))
	}
	sort.Strings(problems)
	return errors.New(strings.Join(problems, "; "))
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// stripCodeFence removes one surrounding markdown code fence, with or
// without a language tag, and trims whitespace.
func stripCodeFence(text string) string {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "```") || !strings.HasSuffix(t, "```") || len(t) < 6 {
		return t
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(t, "```"), "```")
	if nl := strings.Index(inner, "\n"); nl >= 0 && !strings.ContainsAny(inner[:nl], "{[") {
		inner = inner[nl+1:]
	}
	return strings.TrimSpace(inner)
}

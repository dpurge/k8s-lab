package a2ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"assistant/schemas"
	"k8s-lab/shared/yamlschema"
)

// The schemas refer to each other by relative address ("common_types.json#...",
// "catalog.json#..."), so they are registered under one directory, with the
// basic catalog, which names itself under catalogs/basic, moved beside them.
const base = "https://a2ui.org/specification/v1_0/"

// CatalogID is the id of the basic catalog, the one the assistant's pages draw.
const CatalogID = "https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json"

// AssistantCatalogID is the id of the assistant's own catalog of functions
// (schemas/a2ui/assistant_catalog.json): viewport, which the page runs, and
// listWorkflows, which the page asks the assistant for.
const AssistantCatalogID = "https://assistant.k8s-lab.local/a2ui/catalog.json"

// messageKinds are the keys that make an agent-to-renderer message what it is.
var messageKinds = []struct{ key, def string }{
	{"createSurface", "CreateSurfaceMessage"},
	{"updateComponents", "UpdateComponentsMessage"},
	{"updateDataModel", "UpdateDataModelMessage"},
	{"deleteSurface", "DeleteSurfaceMessage"},
	{"callRendererFunction", "CallRendererFunctionMessage"},
	{"agentFunctionResponse", "AgentFunctionResponseMessage"},
}

// Validator checks A2UI messages. It is safe for concurrent use.
type Validator struct {
	limits Limits
	// kinds holds the schema of each message kind, by its key.
	kinds map[string]*jsonschema.Schema
	// event is the schema of a renderer-to-agent message.
	event *jsonschema.Schema
	// components holds, by type, the schema of one component: the envelope every
	// component has, plus the catalog's definition of that type.
	components map[string]*jsonschema.Schema
	// refs says, by component type, where its properties name other components.
	refs map[string][]refPath
	// componentDefs are the catalog's definitions of its components, and order
	// the order its anyComponent lists them in (the guide for a model follows it).
	componentDefs map[string]any
	order         []string
	// functionCallers is, by catalog id and function name, who the catalog lets
	// call it: rendererOnly, agentOnly or rendererOrAgent.
	functionCallers map[string]map[string]string
}

// New compiles the vendored schemas. Pass limits to override the defaults.
func New(limits ...Limits) (*Validator, error) {
	v := &Validator{limits: DefaultLimits}
	if len(limits) > 0 {
		v.limits = limits[0]
	}
	read := func(name string) ([]byte, error) { return schemas.A2UI.ReadFile("a2ui/v1_0/" + name) }

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	add := func(url string, raw []byte) error {
		raw = goRegexp(raw)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("parse %s: %w", url, err)
		}
		return compiler.AddResource(url, doc)
	}
	for _, name := range []string{"common_types.json", "agent_to_renderer.json", "renderer_to_agent.json"} {
		raw, err := read(name)
		if err != nil {
			return nil, err
		}
		if err := add(base+name, raw); err != nil {
			return nil, err
		}
	}
	catalogRaw, err := read("basic_catalog.json")
	if err != nil {
		return nil, err
	}
	var catalog map[string]any
	if err := json.Unmarshal(catalogRaw, &catalog); err != nil {
		return nil, fmt.Errorf("parse the basic catalog: %w", err)
	}
	if catalog["catalogId"] != CatalogID {
		return nil, fmt.Errorf("the vendored basic catalog is %v, not %s", catalog["catalogId"], CatalogID)
	}
	assistantRaw, err := schemas.A2UI.ReadFile("a2ui/assistant_catalog.json")
	if err != nil {
		return nil, err
	}
	var assistant map[string]any
	if err := json.Unmarshal(assistantRaw, &assistant); err != nil {
		return nil, fmt.Errorf("parse the assistant's catalog: %w", err)
	}
	if assistant["catalogId"] != AssistantCatalogID {
		return nil, fmt.Errorf("the assistant's catalog is %v, not %s", assistant["catalogId"], AssistantCatalogID)
	}
	// A function call is checked against one list of the functions there are
	// (the protocol's FunctionCall refers to catalog.json#/$defs/anyFunction), so
	// the assistant's functions join the basic catalog's in the document that
	// stands for it. Which catalog a call names is judged separately.
	basicCallers := callersOf(catalog)
	if err := mergeFunctions(catalog, assistant); err != nil {
		return nil, err
	}
	catalog["$id"] = base + "catalog.json"
	moved, _ := json.Marshal(catalog)
	if err := add(base+"catalog.json", moved); err != nil {
		return nil, err
	}

	if v.event, err = compiler.Compile(base + "renderer_to_agent.json"); err != nil {
		return nil, fmt.Errorf("compile renderer_to_agent: %w", err)
	}
	v.kinds = map[string]*jsonschema.Schema{}
	for _, k := range messageKinds {
		s, err := compiler.Compile(base + "agent_to_renderer.json#/$defs/" + k.def)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", k.def, err)
		}
		v.kinds[k.key] = s
	}

	componentDefs, _ := catalog["components"].(map[string]any)
	v.componentDefs = componentDefs
	if defs, ok := catalog["$defs"].(map[string]any); ok {
		if anyComponent, ok := defs["anyComponent"].(map[string]any); ok {
			list, _ := anyComponent["oneOf"].([]any)
			for _, item := range list {
				ref, _ := item.(map[string]any)["$ref"].(string)
				if name := strings.TrimPrefix(ref, "#/components/"); name != ref {
					v.order = append(v.order, name)
				}
			}
		}
	}
	v.components = map[string]*jsonschema.Schema{}
	v.refs = map[string][]refPath{}
	v.functionCallers = map[string]map[string]string{CatalogID: basicCallers, AssistantCatalogID: callersOf(assistant)}
	for typ, def := range componentDefs {
		doc := map[string]any{
			"allOf": []any{
				map[string]any{"$ref": base + "common_types.json#/$defs/ComponentCommon"},
				map[string]any{"$ref": base + "catalog.json#/components/" + typ},
			},
			"unevaluatedProperties": false,
		}
		url := "mem://component/" + typ + ".json"
		if err := compiler.AddResource(url, doc); err != nil {
			return nil, err
		}
		s, err := compiler.Compile(url)
		if err != nil {
			return nil, fmt.Errorf("compile component %s: %w", typ, err)
		}
		v.components[typ] = s
		v.refs[typ] = childRefs(def)
	}
	return v, nil
}

// goRegexp rewrites the two Unicode properties the schemas use for identifiers,
// XID_Start and XID_Continue, which Go's regular expressions do not have, into
// the classes that cover them: letters and letter numbers to start, and those
// plus marks, digits and connector punctuation to continue. (UAX #31 defines
// the two sets by those categories plus a few exceptions that do not matter for
// the ids and extension names agents write.)
func goRegexp(raw []byte) []byte {
	raw = bytes.ReplaceAll(raw, []byte(`\\p{XID_Start}`), []byte(`\\p{L}\\p{Nl}`))
	return bytes.ReplaceAll(raw, []byte(`\\p{XID_Continue}`), []byte(`\\p{L}\\p{Nl}\\p{Mn}\\p{Mc}\\p{Nd}\\p{Pc}`))
}

// ComponentTypes lists the component types of the catalog, sorted.
func (v *Validator) ComponentTypes() []string {
	out := make([]string, 0, len(v.components))
	for t := range v.components {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// schemaProblems turns a schema failure into problems, from its innermost
// causes (the wrapper nodes of a $ref or an allOf have no message of their own),
// with the path prefixed by where the checked value sits in the message.
func schemaProblems(prefix string, err error) []Problem {
	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []Problem{{Path: prefix, Message: err.Error()}}
	}
	var out []Problem
	seen := map[string]bool{}
	for _, leaf := range yamlschema.Leaves(verr) {
		path := prefix
		if len(leaf.InstanceLocation) > 0 {
			path += "/" + strings.Join(leaf.InstanceLocation, "/")
		}
		msg := leaf.ErrorKind.LocalizedString(yamlschema.Printer)
		if _, isFalse := leaf.ErrorKind.(*kind.FalseSchema); isFalse && len(leaf.InstanceLocation) > 0 {
			// A closed object met a key it does not define.
			msg = fmt.Sprintf("%q is not allowed here", leaf.InstanceLocation[len(leaf.InstanceLocation)-1])
		}
		if key := path + "\x00" + msg; !seen[key] {
			seen[key] = true
			out = append(out, Problem{Path: path, Message: msg})
		}
	}
	return out
}

// decode parses JSON keeping numbers exact.
func decode(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("the message is not a JSON object: %w", err)
	}
	if out == nil {
		return nil, fmt.Errorf("the message is not a JSON object")
	}
	var extra any
	if dec.Decode(&extra) == nil {
		return nil, fmt.Errorf("the message has more than one JSON value")
	}
	return out, nil
}

// EventKinds are the keys that make a renderer-to-agent message what it is.
var EventKinds = []string{"action", "error", "callAgentFunction", "rendererFunctionResponse"}

// callersOf reads, by function name, who a catalog lets call each function; the
// protocol's default is the renderer only.
func callersOf(catalog map[string]any) map[string]string {
	out := map[string]string{}
	functions, _ := catalog["functions"].(map[string]any)
	for name, def := range functions {
		callers := "rendererOnly"
		if m, ok := def.(map[string]any); ok {
			if c, ok := m["allowedCallers"].(string); ok {
				callers = c
			}
		}
		out[name] = callers
	}
	return out
}

// mergeFunctions adds the functions of extra to catalog's functions and to its
// anyFunction list. A name both have is an error: a call would be ambiguous.
func mergeFunctions(catalog, extra map[string]any) error {
	functions, _ := catalog["functions"].(map[string]any)
	more, _ := extra["functions"].(map[string]any)
	defs, _ := catalog["$defs"].(map[string]any)
	any1, _ := defs["anyFunction"].(map[string]any)
	list, _ := any1["oneOf"].([]any)
	names := make([]string, 0, len(more))
	for name := range more {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, taken := functions[name]; taken {
			return fmt.Errorf("the assistant's function %q has the name of a basic catalog function", name)
		}
		functions[name] = more[name]
		list = append(list, map[string]any{"$ref": "#/functions/" + name})
	}
	any1["oneOf"] = list
	return nil
}

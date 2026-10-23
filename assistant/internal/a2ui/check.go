package a2ui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Message is an agent-to-renderer message that passed Check.
type Message struct {
	// Kind is the message's key: createSurface, updateComponents,
	// updateDataModel, deleteSurface, callRendererFunction or
	// agentFunctionResponse.
	Kind string
	// SurfaceID is the surface it is about; empty for a function call that
	// names none, and for a function response.
	SurfaceID string
	// Components are the components it carries, in order.
	Components []map[string]any
	// Body is the value under Kind.
	Body map[string]any
}

// Check refuses a message that is too large, not valid JSON, not valid against
// the protocol's schemas, or carries components the catalog does not define or
// that break the catalog's definition of them. The problems it returns name the
// component and property, so a model can fix them. It looks at one message in
// isolation; Surfaces adds what depends on the messages before it.
func (v *Validator) Check(raw []byte) (*Message, error) {
	if len(raw) > v.limits.MaxMessageBytes {
		return nil, refuse("", "the message is %d bytes; the limit is %d", len(raw), v.limits.MaxMessageBytes)
	}
	msg, err := decode(raw)
	if err != nil {
		return nil, refuse("", "%v", err)
	}

	var kind string
	var extra []string
	for key := range msg {
		if key == "version" {
			continue
		}
		if _, known := v.kinds[key]; known && kind == "" {
			kind = key
		} else {
			extra = append(extra, key)
		}
	}
	if kind == "" || len(extra) > 0 {
		sort.Strings(extra)
		return nil, refuse("", "a message has \"version\": \"v1.0\" and exactly one of createSurface, updateComponents, updateDataModel, deleteSurface, callRendererFunction, agentFunctionResponse (found %s)%s", describeKeys(msg), misplacedBodyHint(msg))
	}
	body, _ := msg[kind].(map[string]any)
	out := &Message{Kind: kind, Body: body}
	if id, ok := body["surfaceId"].(string); ok {
		out.SurfaceID = id
	}

	// The envelope is checked with the components set aside, so that a mistake
	// in one component is reported once, against that component's own type,
	// instead of against all eighteen types at once.
	checked := msg
	if list, ok := body["components"].([]any); ok {
		if len(list) == 0 {
			return nil, refuse("/"+kind+"/components", "a components list needs at least one component")
		}
		for i, c := range list {
			component, ok := c.(map[string]any)
			if !ok {
				return nil, refuse(fmt.Sprintf("/%s/components/%d", kind, i), "a component is a JSON object")
			}
			out.Components = append(out.Components, component)
		}
		if len(list) > v.limits.MaxComponentsPerMessage {
			return nil, refuse(fmt.Sprintf("/%s/components", kind), "the message has %d components; the limit is %d", len(list), v.limits.MaxComponentsPerMessage)
		}
		stand := map[string]any{"id": "x", "component": "Text", "text": "x"}
		checked = copyWith(msg, kind, "components", []any{stand})
	}
	// The function a call names is judged first: every function of the catalog
	// would otherwise be reported against, and the useful answer is that this
	// one may not be called, or does not exist.
	if kind == "callRendererFunction" {
		if call, ok := body["callFunction"].(map[string]any); ok {
			if err := v.checkRendererCall(call, "/callRendererFunction/callFunction"); err != nil {
				return nil, err
			}
		}
	}
	if err := v.kinds[kind].Validate(checked); err != nil {
		return nil, &ValidationError{Problems: schemaProblems("", err)}
	}

	var problems []Problem
	seen := map[string]int{}
	for i, component := range out.Components {
		path := fmt.Sprintf("/%s/components/%d", kind, i)
		typ, _ := component["component"].(string)
		schema, known := v.components[typ]
		if !known {
			problems = append(problems, Problem{Path: path + "/component", Message: fmt.Sprintf("unknown component type %q; the catalog has %s", typ, strings.Join(v.ComponentTypes(), ", "))})
			continue
		}
		if err := schema.Validate(component); err != nil {
			problems = append(problems, schemaProblems(path, err)...)
		}
		if id, ok := component["id"].(string); ok {
			if !identifier.MatchString(id) {
				problems = append(problems, Problem{Path: path + "/id", Message: fmt.Sprintf("the id %q must be an identifier: letters, digits and underscores, not starting with a digit (no hyphens or spaces)", id)})
			}
			if first, dup := seen[id]; dup {
				problems = append(problems, Problem{Path: path + "/id", Message: fmt.Sprintf("the id %q is used twice (also component %d)", id, first)})
			}
			seen[id] = i
		}
	}
	if len(problems) > 0 {
		return nil, &ValidationError{Problems: problems}
	}
	return out, nil
}

func describeKeys(msg map[string]any) string {
	keys := make([]string, 0, len(msg))
	for k := range msg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// misplacedBodyHint explains the commonest slip of a model: closing
// createSurface too early, so that dataModel or components land beside it.
func misplacedBodyHint(msg map[string]any) string {
	if _, ok := msg["createSurface"]; !ok {
		return ""
	}
	var misplaced []string
	for _, key := range []string{"components", "dataModel"} {
		if _, ok := msg[key]; ok {
			misplaced = append(misplaced, fmt.Sprintf("%q", key))
		}
	}
	if len(misplaced) == 0 {
		return ""
	}
	verb := "is"
	if len(misplaced) > 1 {
		verb = "are"
	}
	return fmt.Sprintf(" %s %s next to \"createSurface\" but must be inside it: createSurface was probably closed too early.", strings.Join(misplaced, " and "), verb)
}

// copyWith returns msg with msg[kind][key] replaced, leaving msg untouched.
func copyWith(msg map[string]any, kind, key string, value any) map[string]any {
	out := make(map[string]any, len(msg))
	for k, val := range msg {
		out[k] = val
	}
	body := make(map[string]any)
	for k, val := range msg[kind].(map[string]any) {
		body[k] = val
	}
	body[key] = value
	out[kind] = body
	return out
}

// identifier is what a component id must be: the protocol asks for UAX #31
// identifiers and the page's renderer refuses any other, so a message with one
// would be rejected there after being stored. XID_Start and XID_Continue are
// approximated as in goRegexp.
var identifier = regexp.MustCompile(`^[\p{L}\p{Nl}_][\p{L}\p{Nl}\p{Mn}\p{Mc}\p{Nd}\p{Pc}]*$`)

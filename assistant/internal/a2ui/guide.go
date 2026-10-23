package a2ui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// maxReportedProblems bounds how many problems an error shows a model: the
// first few are the ones to fix, and a long list buries them.
const maxReportedProblems = 8

// maxSignaturesReported bounds how many components a refusal describes: each
// signature is up to about 250 bytes, and two cover the usual mistake.
const maxSignaturesReported = 2

// CheckInterface validates the messages of an interface on their own, as a
// model wrote them: each against the protocol and the catalog, in order, and the
// result as a whole (a root, no missing child). Problems say where, as
// "ui[1]/updateComponents/components/3/text: ...".
func (v *Validator) CheckInterface(messages []json.RawMessage) error {
	surfaces := NewSurfaces(v)
	for i, raw := range messages {
		if _, err := surfaces.Apply(raw); err != nil {
			return v.withAcceptedProperties(raw, prefixed(fmt.Sprintf("ui[%d]", i), err))
		}
	}
	return prefixed("ui", surfaces.Finish())
}

func prefixed(prefix string, err error) error {
	if err == nil {
		return nil
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		return err
	}
	out := &ValidationError{}
	for i, p := range ve.Problems {
		if i == maxReportedProblems {
			out.Problems = append(out.Problems, Problem{Message: fmt.Sprintf("(and %d more problems)", len(ve.Problems)-i)})
			break
		}
		out.Problems = append(out.Problems, Problem{Path: prefix + p.Path, Message: p.Message})
	}
	return out
}

// insideComponent matches the path of a problem about a component of a message:
// "ui[1]/updateComponents/components/3" and what lies below it.
var insideComponent = regexp.MustCompile(`^ui\[\d+\]/(\w+)/components/(\d+)(/.*)?$`)

// withAcceptedProperties adds to the last problem about a component, once per
// component, what that component accepts, so that a model can correct a wrong or
// missing property in one try. raw is the message the problems are about. A
// problem about the id says nothing the signature helps with; an unknown type has
// no signature, and its problem already lists the types of the catalog.
func (v *Validator) withAcceptedProperties(raw []byte, err error) error {
	ve, ok := err.(*ValidationError)
	if !ok {
		return err
	}
	msg, decodeErr := decode(raw)
	if decodeErr != nil {
		return err
	}
	last := map[string]int{} // component name -> index of its last problem
	var order []string
	for i, p := range ve.Problems {
		m := insideComponent.FindStringSubmatch(p.Path)
		if m == nil || strings.HasSuffix(p.Path, "/id") {
			continue
		}
		name := v.componentNameAt(msg, m[1], m[2])
		if name == "" {
			continue
		}
		if _, seen := last[name]; !seen {
			order = append(order, name)
		}
		last[name] = i
	}
	for _, name := range order[:min(len(order), maxSignaturesReported)] {
		p := &ve.Problems[last[name]]
		if !strings.HasSuffix(p.Message, ".") {
			p.Message += "."
		}
		p.Message += fmt.Sprintf(" Accepted for %s: %s.", name, v.signature(name))
	}
	return ve
}

// componentNameAt returns the catalog type of the component at index of the
// message's components, or "" when there is none or the catalog lacks it.
func (v *Validator) componentNameAt(msg map[string]any, kind, index string) string {
	body, _ := msg[kind].(map[string]any)
	list, _ := body["components"].([]any)
	i, convErr := strconv.Atoi(index)
	if convErr != nil || i >= len(list) {
		return ""
	}
	component, _ := list[i].(map[string]any)
	name, _ := component["component"].(string)
	if _, known := v.components[name]; !known {
		return ""
	}
	return name
}

// InterfaceSchema is the JSON Schema of the "ui" value of an ask_user payload:
// one createSurface message with its components. A model's reply is decoded
// under it, so the brackets of the interface come out right; what each component
// may hold stays with CheckInterface, so a component only needs an id and a type
// here.
func (v *Validator) InterfaceSchema() map[string]any {
	types := v.ComponentTypes()
	names := make([]any, len(types))
	for i, t := range types {
		names[i] = t
	}
	component := map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"required":             []any{"id", "component"},
		"properties": map[string]any{
			"id":        map[string]any{"type": "string"},
			"component": map[string]any{"enum": names},
		},
	}
	createSurface := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"surfaceId", "catalogId", "components"},
		"properties": map[string]any{
			"surfaceId":     map[string]any{"type": "string", "minLength": 1},
			"catalogId":     map[string]any{"const": CatalogID},
			"components":    map[string]any{"type": "array", "minItems": 1, "items": component},
			"dataModel":     map[string]any{"type": "object"},
			"sendDataModel": map[string]any{"type": "boolean"},
			"metadata":      map[string]any{"type": "object"},
		},
	}
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"maxItems": 1,
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"version", "createSurface"},
			"properties": map[string]any{
				"version":       map[string]any{"const": "v1.0"},
				"createSurface": createSurface,
			},
		},
	}
}

// GuideExample is the complete reply the guide shows a model; a test keeps it
// valid, so that the guide never teaches a mistake.
const GuideExample = `{"state_patch":{},"action":{"type":"ask_user","payload":{"question":"Tell me where to send it.","ui":[{"version":"v1.0","createSurface":{"surfaceId":"contact","catalogId":"` + CatalogID + `","components":[{"id":"root","component":"Card","child":"col"},{"id":"col","component":"Column","children":["title","name","topics","send"]},{"id":"title","component":"Text","text":"## Contact"},{"id":"name","component":"TextField","label":"Name","value":{"@path":"/form/name"},"checks":[{"condition":{"@call":"required","args":{"value":{"@path":"/form/name"}}},"message":"Name is required"}]},{"id":"topics","component":"ChoicePicker","label":"Topics","variant":"multipleSelection","options":[{"label":"News","value":"news"},{"label":"Offers","value":"offers"}],"value":{"@path":"/form/topics"}},{"id":"send","component":"Button","child":"send_label","variant":"primary","action":{"event":{"name":"send","context":{"name":{"@path":"/form/name"},"topics":{"@path":"/form/topics"}}}}},{"id":"send_label","component":"Text","text":"Send"}],"dataModel":{"form":{"name":"","topics":[]}}}}]}}}`

const guideHead = `INTERFACES
This workflow may show the user an interface (A2UI v1.0) instead of plain text. Put it in the ask_user payload under "ui", next to a short "question", as an array with ONE message: {"version":"v1.0","createSurface":{"surfaceId":"<a short name>","catalogId":"` + CatalogID + `","components":[...],"dataModel":{...}}}.
Rules:
- "components" is a flat list. Every component has "id" (letters, digits and underscores only: no hyphens, no spaces) and "component" (its type). One has the id "root". A container names its children by id ("children":["a","b"], or "child":"a"); every child must be in the list; nothing is nested inline.
- Use only the components below and only their properties. Do not invent properties.
- Text may use simple Markdown (**bold**, a "## " heading, "- " lists). A property that takes a string, number or boolean can be fixed ("text":"Hello") or bound to the data model ({"@path":"/form/name"}). "dataModel" holds the initial values; an input's "value" is bound to a path in it, and what the user types is written back to that path.
- A Button has "child" (the id of a Text) and "action":{"event":{"name":"<name>","context":{"<key>":{"@path":"/form/x"}}}}. When the user presses it, the workflow's next observation is {"ui_action":{"name":"<name>","context":{...}}} with the context's values filled in. Name what you need in the context.
- To require a value, put "checks":[{"condition":{"@call":"required","args":{"value":{"@path":"/form/x"}}},"message":"X is required"}] on the input and on the Button.
- Ask one thing per interface, keep it small, and finish with a Button.
Components (a name? is optional; "a|b" means one of):`

// Guide is the part of the system prompt that teaches a model to write an
// interface: the rules, the components of the catalog with their properties, and
// one complete reply. It is built from the catalog itself, so it cannot drift
// from what the validator accepts.
func (v *Validator) Guide() string {
	var b strings.Builder
	b.WriteString(guideHead)
	b.WriteString("\n")
	for _, name := range v.order {
		fmt.Fprintf(&b, "- %s: %s\n", name, v.signature(name))
	}
	b.WriteString("Complete reply, as one line:\n")
	b.WriteString(GuideExample)
	return b.String()
}

// signature lists a component's properties, each with the kind of value it
// takes; a trailing ? marks an optional one.
func (v *Validator) signature(name string) string {
	def, _ := v.componentDefs[name].(map[string]any)
	required := map[string]bool{}
	props := map[string]any{}
	var collect func(node any)
	collect = func(node any) {
		m, ok := node.(map[string]any)
		if !ok {
			return
		}
		if p, ok := m["properties"].(map[string]any); ok {
			for k, val := range p {
				props[k] = val
			}
		}
		if r, ok := m["required"].([]any); ok {
			for _, x := range r {
				if s, ok := x.(string); ok {
					required[s] = true
				}
			}
		}
		if all, ok := m["allOf"].([]any); ok {
			for _, part := range all {
				if ref, _ := part.(map[string]any)["$ref"].(string); strings.HasSuffix(ref, "/Checkable") {
					props["checks"] = map[string]any{"checks": true}
					continue
				}
				collect(part)
			}
		}
	}
	collect(def)
	names := make([]string, 0, len(props))
	for k := range props {
		if k == "component" || k == "weight" || k == "accessibility" {
			continue
		}
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if required[names[i]] != required[names[j]] {
			return required[names[i]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, k := range names {
		mark := ""
		if !required[k] {
			mark = "?"
		}
		parts[i] = fmt.Sprintf("%s%s %s", k, mark, kindOf(props[k]))
	}
	return strings.Join(parts, ", ")
}

// kindOf describes in a few words what a property takes.
func kindOf(schema any) string {
	m, ok := schema.(map[string]any)
	if !ok {
		return ""
	}
	if _, ok := m["checks"]; ok {
		return "[{condition, message}]"
	}
	if ref, ok := m["$ref"].(string); ok {
		switch ref[strings.LastIndex(ref, "/")+1:] {
		case "DynamicString":
			return "(text or binding)"
		case "DynamicNumber":
			return "(number or binding)"
		case "DynamicBoolean":
			return "(true/false or binding)"
		case "DynamicStringList":
			return "(list of strings or binding)"
		case "Child":
			return "(component id)"
		case "ChildList":
			return `(list of ids, or {"componentId":id,"path":"/list"} to repeat one for each item)`
		case "Action":
			return "(action)"
		case "DataBinding":
			return "(binding)"
		}
		return "(value)"
	}
	if enum, ok := m["enum"].([]any); ok {
		parts := make([]string, len(enum))
		for i, e := range enum {
			parts[i] = fmt.Sprint(e)
		}
		return strings.Join(parts, "|")
	}
	if oneOf, ok := m["oneOf"].([]any); ok && len(oneOf) > 0 {
		// An icon name: an enum, or an object; show the first enum.
		for _, o := range oneOf {
			if e, ok := o.(map[string]any)["enum"].([]any); ok && len(e) > 0 {
				return fmt.Sprintf("(one of %d names, e.g. %v, %v, %v)", len(e), e[0], e[1], e[2])
			}
		}
	}
	switch m["type"] {
	case "array":
		if items, ok := m["items"].(map[string]any); ok && items["type"] == "object" {
			props, _ := items["properties"].(map[string]any)
			keys := make([]string, 0, len(props))
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = k + ": " + kindOf(props[k])
			}
			return "[{" + strings.Join(parts, ", ") + "}]"
		}
		return "[...]"
	case "number", "integer", "boolean", "string":
		return fmt.Sprint(m["type"])
	}
	return ""
}

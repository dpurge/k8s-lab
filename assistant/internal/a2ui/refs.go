package a2ui

// refPath is where a component's properties name other components: keys to
// follow, "[]" meaning every element of an array, and whether the value is one
// child, or a child list (an array of ids, or a template over a data list).
type refPath struct {
	steps []string
	list  bool
}

const (
	childRef     = "common_types.json#/$defs/Child"
	childListRef = "common_types.json#/$defs/ChildList"
)

// childRefs finds the properties of a catalog component definition that refer
// to other components, by reading the definition itself: a property whose
// schema is the protocol's Child or ChildList, also inside arrays of objects
// (Tabs) and inside allOf wrappers.
func childRefs(def any) []refPath {
	var out []refPath
	var walk func(node any, steps []string)
	walk = func(node any, steps []string) {
		m, ok := node.(map[string]any)
		if !ok {
			return
		}
		if ref, _ := m["$ref"].(string); ref == childRef || ref == childListRef {
			out = append(out, refPath{steps: append([]string(nil), steps...), list: ref == childListRef})
			return
		}
		if all, ok := m["allOf"].([]any); ok {
			for _, part := range all {
				walk(part, steps)
			}
		}
		if props, ok := m["properties"].(map[string]any); ok {
			for name, sub := range props {
				walk(sub, append(append([]string(nil), steps...), name))
			}
		}
		if items, ok := m["items"]; ok {
			walk(items, append(append([]string(nil), steps...), "[]"))
		}
	}
	walk(def, nil)
	return out
}

// referencesOf lists the component ids a component names, in a stable order.
func referencesOf(component map[string]any, paths []refPath) []string {
	var out []string
	var follow func(value any, steps []string, list bool)
	follow = func(value any, steps []string, list bool) {
		if len(steps) == 0 {
			switch x := value.(type) {
			case string:
				out = append(out, x)
			case []any:
				if list {
					for _, e := range x {
						if id, ok := e.(string); ok {
							out = append(out, id)
						}
					}
				}
			case map[string]any:
				// A template: one component, drawn once per item of a data list.
				if list {
					if id, ok := x["componentId"].(string); ok {
						out = append(out, id)
					}
				}
			}
			return
		}
		if steps[0] == "[]" {
			if items, ok := value.([]any); ok {
				for _, item := range items {
					follow(item, steps[1:], list)
				}
			}
			return
		}
		if m, ok := value.(map[string]any); ok {
			if next, ok := m[steps[0]]; ok {
				follow(next, steps[1:], list)
			}
		}
	}
	for _, p := range paths {
		follow(component, p.steps, p.list)
	}
	return out
}

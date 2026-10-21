package a2ui

import (
	"fmt"
	"sort"
	"strings"
)

type surface struct {
	components map[string]map[string]any
	// touched is whether components changed since Finish last checked the tree.
	touched bool
}

// Surfaces follows the surfaces a conversation has drawn, message by message,
// and refuses a message that does not fit what came before it: an update for a
// surface that does not exist, a surface created twice, a component tree with a
// cycle or too deep, too many components, a function call id used twice.
// It is not safe for concurrent use; one conversation's messages are applied in
// order by one goroutine.
type Surfaces struct {
	v        *Validator
	surfaces map[string]*surface
	// deleted keeps ids that were used: a surface id is unique for the
	// renderer's whole life, so a deleted one is not drawn again.
	deleted map[string]bool
	calls   map[string]bool
}

// NewSurfaces starts with nothing drawn.
func NewSurfaces(v *Validator) *Surfaces {
	return &Surfaces{v: v, surfaces: map[string]*surface{}, deleted: map[string]bool{}, calls: map[string]bool{}}
}

// Has reports whether the surface exists.
func (s *Surfaces) Has(id string) bool { _, ok := s.surfaces[id]; return ok }

// Used reports whether a surface id is taken: it exists, or it did and was
// deleted (an id is used once for the life of the conversation).
func (s *Surfaces) Used(id string) bool { return s.Has(id) || s.deleted[id] }

// Components returns the ids of a surface's components, sorted.
func (s *Surfaces) Components(id string) []string {
	sf, ok := s.surfaces[id]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(sf.components))
	for c := range sf.components {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Apply checks a message against the protocol and against the surfaces so far,
// and when it passes, records its effect. A message that does not pass changes
// nothing.
func (s *Surfaces) Apply(raw []byte) (*Message, error) {
	m, err := s.v.Check(raw)
	if err != nil {
		return nil, err
	}
	path := "/" + m.Kind
	switch m.Kind {
	case "createSurface":
		if s.Has(m.SurfaceID) || s.deleted[m.SurfaceID] {
			return nil, refuse(path+"/surfaceId", "the surface %q already exists or existed; a surface id is used once", m.SurfaceID)
		}
		if id, _ := m.Body["catalogId"].(string); id != CatalogID {
			return nil, refuse(path+"/catalogId", "the catalog %q is not available; use %s", id, CatalogID)
		}
		// A new surface is checked for completeness whatever it carries.
		sf := &surface{components: map[string]map[string]any{}, touched: true}
		if err := s.v.merge(sf, m, path); err != nil {
			return nil, err
		}
		s.surfaces[m.SurfaceID] = sf
	case "updateComponents":
		sf, ok := s.surfaces[m.SurfaceID]
		if !ok {
			return nil, refuse(path+"/surfaceId", "there is no surface %q; create it with createSurface first", m.SurfaceID)
		}
		next := &surface{components: copyComponents(sf.components), touched: true}
		if err := s.v.merge(next, m, path); err != nil {
			return nil, err
		}
		s.surfaces[m.SurfaceID] = next
	case "updateDataModel":
		if !s.Has(m.SurfaceID) {
			return nil, refuse(path+"/surfaceId", "there is no surface %q", m.SurfaceID)
		}
	case "deleteSurface":
		if !s.Has(m.SurfaceID) {
			return nil, refuse(path+"/surfaceId", "there is no surface %q", m.SurfaceID)
		}
		delete(s.surfaces, m.SurfaceID)
		s.deleted[m.SurfaceID] = true
	case "callRendererFunction":
		if m.SurfaceID != "" && !s.Has(m.SurfaceID) {
			return nil, refuse(path+"/surfaceId", "there is no surface %q", m.SurfaceID)
		}
		id, _ := m.Body["functionCallId"].(string)
		if s.calls[id] {
			return nil, refuse(path+"/functionCallId", "the call id %q was already used; every call needs a new one", id)
		}
		s.calls[id] = true
	}
	return m, nil
}

// Finish checks that every surface changed since the last call is complete: it
// has a root, and every component a component names exists. Messages may arrive
// in pieces, so completeness is asked for when the agent has said its piece.
func (s *Surfaces) Finish() error {
	ids := make([]string, 0, len(s.surfaces))
	for id := range s.surfaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var problems []Problem
	for _, id := range ids {
		sf := s.surfaces[id]
		if !sf.touched {
			continue
		}
		for _, p := range s.v.treeProblems(sf.components, true) {
			p.Message = fmt.Sprintf("surface %q: %s", id, p.Message)
			problems = append(problems, p)
		}
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	for _, sf := range s.surfaces {
		sf.touched = false
	}
	return nil
}

func copyComponents(in map[string]map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// merge puts a message's components into the surface and checks the result.
func (v *Validator) merge(sf *surface, m *Message, path string) error {
	for _, c := range m.Components {
		sf.components[c["id"].(string)] = c
	}
	if len(sf.components) > v.limits.MaxComponentsPerSurface {
		return refuse(path+"/components", "the surface would have %d components; the limit is %d", len(sf.components), v.limits.MaxComponentsPerSurface)
	}
	sf.touched = sf.touched || len(m.Components) > 0
	if problems := v.treeProblems(sf.components, false); len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// checkRendererCall refuses a call to a function the page may not run for an
// agent: one the named catalog does not have, or one it keeps for the renderer.
// The basic catalog has none an agent may call; the assistant's has viewport.
func (v *Validator) checkRendererCall(call map[string]any, path string) error {
	id, _ := call["catalogId"].(string)
	callers, known := v.functionCallers[id]
	if !known {
		return refuse(path+"/catalogId", "the catalog %q is not available; use %s or %s", id, CatalogID, AssistantCatalogID)
	}
	name, _ := call["@call"].(string)
	allowed, exists := callers[name]
	if !exists {
		return refuse(path+"/@call", "the catalog %s has no function %q", id, name)
	}
	if allowed == "rendererOnly" {
		return refuse(path+"/@call", "the function %q may be called only by the renderer, not by an agent", name)
	}
	return nil
}

// treeProblems checks the component tree: no cycle, and no more levels than
// the limit. When complete, also that there is a root and no component names
// one that does not exist.
func (v *Validator) treeProblems(components map[string]map[string]any, complete bool) []Problem {
	edges := make(map[string][]string, len(components))
	ids := make([]string, 0, len(components))
	for id, c := range components {
		ids = append(ids, id)
		typ, _ := c["component"].(string)
		edges[id] = referencesOf(c, v.refs[typ])
	}
	sort.Strings(ids)

	var problems []Problem
	if complete {
		if _, ok := components["root"]; !ok {
			problems = append(problems, Problem{Message: `there is no component with the id "root"`})
		}
		for _, id := range ids {
			for _, ref := range edges[id] {
				if _, ok := components[ref]; !ok {
					problems = append(problems, Problem{Message: fmt.Sprintf("component %q names %q, which does not exist", id, ref)})
				}
			}
		}
	}

	// Cycles and depth, walking from every component so that a loop that does
	// not touch the root is found too.
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	depth := map[string]int{}
	var stack []string
	var visit func(id string) bool
	visit = func(id string) bool {
		switch state[id] {
		case visiting:
			i := 0
			for j, s := range stack {
				if s == id {
					i = j
				}
			}
			problems = append(problems, Problem{Message: fmt.Sprintf("components form a cycle: %s", strings.Join(append(append([]string(nil), stack[i:]...), id), " > "))})
			return false
		case done:
			return true
		}
		state[id] = visiting
		stack = append(stack, id)
		longest := 0
		ok := true
		for _, ref := range edges[id] {
			if _, exists := components[ref]; !exists {
				continue
			}
			if !visit(ref) {
				ok = false
				break
			}
			if depth[ref] > longest {
				longest = depth[ref]
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = done
		depth[id] = longest + 1
		return ok
	}
	for _, id := range ids {
		if state[id] == 0 && !visit(id) {
			return problems
		}
	}
	if _, ok := components["root"]; ok && depth["root"] > v.limits.MaxDepth {
		problems = append(problems, Problem{Message: fmt.Sprintf("the tree is %d levels deep; the limit is %d", depth["root"], v.limits.MaxDepth)})
	}
	return problems
}

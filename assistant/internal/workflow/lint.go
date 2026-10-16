package workflow

import (
	"fmt"
	"sort"
	"strings"

	"assistant/internal/yamlschema"
)

// Lint checks a loaded workflow for mistakes the file format cannot express:
// dangling references, states a run can never leave or reach, and a state
// schema that is not valid JSON Schema. tools may be nil to skip the check
// of tool names. Findings are ordered by line, then path.
func Lint(wf *Workflow, tools ToolSet) []Finding {
	var findings []Finding
	add := func(message string, segments ...string) {
		findings = append(findings, Finding{
			Path:    yamlschema.FormatPath(segments),
			Line:    wf.line(segments...),
			Message: message,
		})
	}

	lintLimits(wf, add)
	lintTags(wf.ModelTags, add, "model_tags")
	lintStateSchema(wf, add)

	if _, ok := wf.States[wf.Start]; !ok {
		add(fmt.Sprintf("start state %q does not exist", wf.Start), "start")
	}

	names := sortedStateNames(wf)
	hasEnd := false
	for _, name := range names {
		st := wf.States[name]
		lintTags(st.ModelTags, add, "states", name, "model_tags")
		if st.End {
			hasEnd = true
			if len(st.Next) > 0 {
				add(fmt.Sprintf("end state %q must not have next states", name), "states", name, "next")
			}
		}
		for _, target := range st.Next {
			if _, ok := wf.States[target]; !ok {
				add(fmt.Sprintf("state %q lists next state %q which does not exist", name, target), "states", name, "next")
			}
		}
		if st.OnError != "" {
			if _, ok := wf.States[st.OnError]; !ok {
				add(fmt.Sprintf("state %q has on_error state %q which does not exist", name, st.OnError), "states", name, "on_error")
			}
		}
		if tools != nil {
			for _, tool := range st.Tools {
				if !tools.Has(tool) {
					add(fmt.Sprintf("state %q uses unknown tool %q", name, tool), "states", name, "tools")
				}
			}
		}
	}

	if !hasEnd {
		add("no state has end: true, so a run can never succeed", "states")
	}
	lintReachability(wf, names, add)

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Path < findings[j].Path
	})
	return findings
}

// lintTags reports a tag that is blank or repeats another once whitespace is
// trimmed; the schema already refuses an empty string and an exact repeat.
func lintTags(tags []string, add func(string, ...string), path ...string) {
	seen := map[string]bool{}
	for _, tag := range tags {
		trimmed := strings.TrimSpace(tag)
		switch {
		case trimmed == "":
			add("model tag is blank", path...)
		case seen[trimmed]:
			add(fmt.Sprintf("model tag %q is listed twice", trimmed), path...)
		}
		seen[trimmed] = true
	}
}

func lintLimits(wf *Workflow, add func(string, ...string)) {
	checks := []struct {
		key   string
		value int
		min   int
	}{
		{"max_steps", wf.Limits.MaxSteps, 1},
		{"max_tokens", wf.Limits.MaxTokens, 1},
		{"max_retries", wf.Limits.MaxRetries, 0},
		{"max_visits", wf.Limits.MaxVisits, 1},
	}
	for _, c := range checks {
		if c.value < c.min {
			add(fmt.Sprintf("limit %s must be at least %d, got %d", c.key, c.min, c.value), "limits", c.key)
		}
	}
}

func lintStateSchema(wf *Workflow, add func(string, ...string)) {
	if _, err := NewStateValidator(wf.StateSchema); err != nil {
		add(err.Error(), "state_schema")
	}
}

// lintReachability reports states a run cannot reach from the start and
// states from which no end state can be reached. Both next and on_error
// edges count as ways to move.
func lintReachability(wf *Workflow, names []string, add func(string, ...string)) {
	edges := func(name string) []string {
		st := wf.States[name]
		out := append([]string{}, st.Next...)
		if st.OnError != "" {
			out = append(out, st.OnError)
		}
		return out
	}

	reached := map[string]bool{}
	if _, ok := wf.States[wf.Start]; ok {
		queue := []string{wf.Start}
		reached[wf.Start] = true
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, nxt := range edges(cur) {
				if _, ok := wf.States[nxt]; ok && !reached[nxt] {
					reached[nxt] = true
					queue = append(queue, nxt)
				}
			}
		}
		for _, name := range names {
			if !reached[name] {
				add(fmt.Sprintf("state %q cannot be reached from start state %q", name, wf.Start), "states", name)
			}
		}
	}

	// Walk the edges backwards from every end state.
	reverse := map[string][]string{}
	for _, name := range names {
		for _, nxt := range edges(name) {
			reverse[nxt] = append(reverse[nxt], name)
		}
	}
	canFinish := map[string]bool{}
	var queue []string
	for _, name := range names {
		if wf.States[name].End {
			canFinish[name] = true
			queue = append(queue, name)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, prev := range reverse[cur] {
			if !canFinish[prev] {
				canFinish[prev] = true
				queue = append(queue, prev)
			}
		}
	}
	for _, name := range names {
		if !canFinish[name] && reached[name] {
			add(fmt.Sprintf("state %q cannot reach an end state", name), "states", name)
		}
	}
}

func sortedStateNames(wf *Workflow) []string {
	names := make([]string, 0, len(wf.States))
	for n := range wf.States {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

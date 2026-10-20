// Package yamlschema checks a YAML file against a JSON Schema and reports every
// problem with the key path and the line it is on. The assistant's workflow and
// model configuration files both use it, so a mistake in either is pointed out
// the same way.
package yamlschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"go.yaml.in/yaml/v3"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Printer words schema violations in English.
var Printer = message.NewPrinter(language.English)

// Finding is one problem in a file, located by key path and line.
type Finding struct {
	Path    string
	Line    int
	Message string
}

// Error reports every problem found in one file.
type Error struct {
	Name     string
	Findings []Finding
}

func (e *Error) Error() string {
	lines := make([]string, 0, len(e.Findings))
	for _, f := range e.Findings {
		lines = append(lines, fmt.Sprintf("%s:%d: %s: %s", e.Name, f.Line, f.Path, f.Message))
	}
	return strings.Join(lines, "\n")
}

// Schema is a compiled JSON Schema.
type Schema struct {
	compiled *jsonschema.Schema
}

// Compile compiles a JSON Schema document. resourceURL only names it in
// messages.
func Compile(schemaJSON []byte, resourceURL string) (*Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("parse schema %s: %w", resourceURL, err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(resourceURL, doc); err != nil {
		return nil, fmt.Errorf("add schema %s: %w", resourceURL, err)
	}
	compiled, err := compiler.Compile(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("compile schema %s: %w", resourceURL, err)
	}
	return &Schema{compiled: compiled}, nil
}

// Check parses data as YAML and validates it. name is only used in messages.
// It returns the parsed document, which LineOf can search, and an *Error
// listing every problem when the file is not valid YAML or breaks the schema.
func (s *Schema) Check(name string, data []byte) (*yaml.Node, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, &Error{Name: name, Findings: []Finding{syntaxFinding(err)}}
	}
	if root.Kind == 0 {
		return nil, &Error{Name: name, Findings: []Finding{{Path: "(file)", Line: 1, Message: "file is empty"}}}
	}

	var doc any
	if err := root.Decode(&doc); err != nil {
		return nil, &Error{Name: name, Findings: []Finding{syntaxFinding(err)}}
	}
	asJSON, err := json.Marshal(doc)
	if err != nil {
		return nil, &Error{Name: name, Findings: []Finding{{
			Path: "(file)", Line: 1, Message: fmt.Sprintf("not representable as JSON (are all keys strings?): %v", err),
		}}}
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		return nil, fmt.Errorf("convert %s to JSON: %w", name, err)
	}
	if err := s.compiled.Validate(instance); err != nil {
		var verr *jsonschema.ValidationError
		if !errors.As(err, &verr) {
			return nil, fmt.Errorf("validate %s: %w", name, err)
		}
		return nil, &Error{Name: name, Findings: findings(&root, verr)}
	}
	return &root, nil
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

func syntaxFinding(err error) Finding {
	line := 1
	if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
		line, _ = strconv.Atoi(m[1])
	}
	return Finding{Path: "(file)", Line: line, Message: strings.TrimPrefix(err.Error(), "yaml: ")}
}

func findings(root *yaml.Node, verr *jsonschema.ValidationError) []Finding {
	var out []Finding
	seen := map[string]bool{}
	add := func(segments []string, key string, msg string) {
		all := append(append([]string{}, segments...), keyIf(key)...)
		f := Finding{Path: FormatPath(all), Line: LineOf(root, all), Message: msg}
		id := fmt.Sprintf("%s|%d|%s", f.Path, f.Line, f.Message)
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, f)
	}

	for _, leaf := range Leaves(verr) {
		segments := leaf.InstanceLocation
		switch k := leaf.ErrorKind.(type) {
		case *kind.AdditionalProperties:
			for _, prop := range k.Properties {
				add(segments, prop, fmt.Sprintf("unknown key %q", prop))
			}
		case *kind.Required:
			for _, prop := range k.Missing {
				add(segments, "", fmt.Sprintf("missing required key %q", prop))
			}
		default:
			add(segments, "", leaf.ErrorKind.LocalizedString(Printer))
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func keyIf(key string) []string {
	if key == "" {
		return nil
	}
	return []string{key}
}

// Leaves returns the innermost causes of a validation error: wrapper nodes
// (schema, $ref) carry no message of their own.
func Leaves(e *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(e.Causes) == 0 {
		return []*jsonschema.ValidationError{e}
	}
	var leaves []*jsonschema.ValidationError
	for _, c := range e.Causes {
		leaves = append(leaves, Leaves(c)...)
	}
	return leaves
}

// FormatPath writes a key path with dots; the empty path is "(root)".
func FormatPath(segments []string) string {
	if len(segments) == 0 {
		return "(root)"
	}
	return strings.Join(segments, ".")
}

// LineOf returns the line of the node at the key path, or of the deepest node
// that exists on it. Mapping keys report their own line.
func LineOf(root *yaml.Node, segments []string) int {
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	line := node.Line
	for _, seg := range segments {
		next, ok := child(node, seg)
		if !ok {
			break
		}
		node = next.value
		line = next.line
	}
	return line
}

type childNode struct {
	value *yaml.Node
	line  int
}

func child(node *yaml.Node, seg string) (childNode, bool) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == seg {
				return childNode{value: node.Content[i+1], line: node.Content[i].Line}, true
			}
		}
	case yaml.SequenceNode:
		if idx, err := strconv.Atoi(seg); err == nil && idx >= 0 && idx < len(node.Content) {
			return childNode{value: node.Content[idx], line: node.Content[idx].Line}, true
		}
	}
	return childNode{}, false
}

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// decodeStrict decodes the config file into fc with Decoder.KnownFields(true),
// so that a key the config does not know (a misspelt num_ctx for numCtx) is
// not silently ignored, and returns one finding for each such key, naming its
// full path and line. A key that legacyKeyFindings already explains is left to
// that more specific message. Any other decode error, such as a value of the
// wrong type, is returned as err.
//
// The decoder's own unknown-field message names the key but not where it is,
// so the findings come from walking the parsed document against fileConfig;
// KnownFields is what keeps the decode itself from passing over the keys.
func decodeStrict(data []byte, fc *fileConfig) (unknown []string, err error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// An empty file is io.EOF here but fine for yaml.Unmarshal: defaults apply.
	if err := dec.Decode(fc); err != nil && !errors.Is(err, io.EOF) {
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			return nil, err
		}
		var other []string
		for _, message := range typeErr.Errors {
			if !strings.Contains(message, "not found in type") {
				other = append(other, message)
			}
		}
		if len(other) > 0 {
			return nil, &yaml.TypeError{Errors: other}
		}
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if top := mappingOf(&root); top != nil {
		unknown = unknownKeyFindings(top, reflect.TypeOf(*fc), "")
	}
	return unknown, nil
}

// unknownKeyFindings walks a mapping node against the yaml tags of the struct
// type t and reports every key that is not a field, at any depth.
func unknownKeyFindings(node *yaml.Node, t reflect.Type, path string) []string {
	if node.Kind != yaml.MappingNode || t.Kind() != reflect.Struct {
		return nil
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
			fields[name] = t.Field(i).Type
		}
	}
	var findings []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		keyPath := key.Value
		if path != "" {
			keyPath = path + "." + key.Value
		}
		fieldType, known := fields[key.Value]
		switch {
		case known:
			findings = append(findings, unknownKeyFindings(node.Content[i+1], fieldType, keyPath)...)
		case !isLegacyKey(keyPath):
			findings = append(findings, fmt.Sprintf("%s: unknown key (line %d); check its spelling", keyPath, key.Line))
		}
	}
	return findings
}

// isLegacyKey says whether legacyKeyFindings has its own message for the key at
// path.
func isLegacyKey(path string) bool {
	switch path {
	case "providers", "llmCalls", "llm_calls.retentionDays", "jobs.retentionDaysDone", "jobs.retentionDaysFailed":
		return true
	}
	for _, p := range (fileConfig{}).purposes() {
		for _, key := range []string{"modelTags", "provider", "model", "think"} {
			if path == p.name+"."+key {
				return true
			}
		}
	}
	return false
}

package ops

import (
	"embed"
	"encoding/json"
	"fmt"

	"k8s-lab/shared/message"
)

// The operation names.
const (
	DocumentsSearch = "documents_search"
	URLIngest       = "url_ingest"
	TaskStatus      = "task_status"
)

//go:embed schemas/*.json
var schemaFiles embed.FS

// operationNames is the catalogue, in the order Register uses when it is given
// no names.
var operationNames = []string{DocumentsSearch, URLIngest, TaskStatus}

// Names returns the operation names of the catalogue.
func Names() []string {
	return append([]string(nil), operationNames...)
}

// Schemas returns the JSON Schemas (draft 2020-12) of the request payload and of
// the response payload of an operation. ok is false for a name that is not in
// the catalogue.
func Schemas(name string) (request, response json.RawMessage, ok bool) {
	if !isKnown(name) {
		return nil, nil, false
	}
	return readSchema(name, "request"), readSchema(name, "response"), true
}

func isKnown(name string) bool {
	for _, known := range operationNames {
		if known == name {
			return true
		}
	}
	return false
}

// readSchema reads an embedded file; a missing one is a build mistake, caught by
// the tests, so it panics.
func readSchema(name, side string) json.RawMessage {
	data, err := schemaFiles.ReadFile("schemas/" + name + "." + side + ".json")
	if err != nil {
		panic(fmt.Sprintf("ops: embedded schema %s %s is missing: %v", name, side, err))
	}
	return data
}

// Register adds the named operations to the registry, with their schemas. With
// no names it adds all of them. A responder registers only what it answers; a
// client registers all. It fails on a name that is not in the catalogue (before
// it registers anything) and on a name the registry already has.
func Register(reg *message.Registry, names ...string) error {
	if len(names) == 0 {
		names = operationNames
	}
	for _, name := range names {
		if !isKnown(name) {
			return fmt.Errorf("register ops: %q is not an operation of the catalogue (known: %v)", name, operationNames)
		}
	}
	for _, name := range names {
		request, response, _ := Schemas(name)
		if err := reg.Register(message.Operation{Name: name, Request: request, Response: response}); err != nil {
			return err
		}
	}
	return nil
}

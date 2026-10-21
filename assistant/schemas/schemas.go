// Package schemas embeds the JSON Schema files that are the assistant's
// contracts, so code can validate against the same files a human reads.
package schemas

import (
	"embed"
)

// Workflow is the JSON Schema of a workflow YAML file.
//
//go:embed workflow.schema.json
var Workflow []byte

// DB is the database schema, idempotent DDL.
//
//go:embed db/schema.sql
var DB string

// OpenAPI is the HTTP API's description, OpenAPI 3.1.
//
//go:embed api/openapi.yaml
var OpenAPI []byte

// A2UI holds the vendored A2UI v1.0 JSON schemas (see a2ui/v1_0/SOURCE.md for
// where they come from): the messages in both directions, the common types and
// the basic catalog; and the assistant's own catalog of functions
// (a2ui/assistant_catalog.json).
//
//go:embed a2ui/v1_0/*.json a2ui/assistant_catalog.json
var A2UI embed.FS

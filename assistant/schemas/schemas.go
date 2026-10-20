// Package schemas embeds the JSON Schema files that are the assistant's
// contracts, so code can validate against the same files a human reads.
package schemas

import _ "embed"

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

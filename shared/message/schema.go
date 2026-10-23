package message

import (
	_ "embed"
)

//go:embed envelope.schema.json
var envelopeSchema []byte

// EnvelopeSchema returns the JSON Schema (draft 2020-12) of the envelope. It
// states the same rules Validate enforces as far as JSON Schema can: Validate
// alone checks that a request's correlation_id equals its id and that
// created_at is a real time, and it reads the payload of a request or a response
// through the registry.
func EnvelopeSchema() []byte {
	return append([]byte(nil), envelopeSchema...)
}

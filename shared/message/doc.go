// Package message is the one structured message format the apps use to call
// each other: a JSON envelope with a typed error, a registry of the operations
// an app answers, a JSON Schema of the envelope, and a one-line Render for logs
// and admin pages. The transport (NATS) is not part of this package.
//
// # The envelope
//
// Every message is one JSON object with snake_case keys and only these fields
// (anything else is rejected: this is schema_version 1):
//
//	schema_version  1
//	id              UUID of this message (lower case)
//	type            request, response, event or error
//	correlation_id  UUID of the request this message belongs to; a request's own id
//	from, to        app names: 1 to 64 characters of a-z, 0-9, _ and -
//	created_at      RFC 3339 time in UTC, ending in Z
//	operation       what is asked: a-z, 0-9 and _, starting with a letter, at most 64
//	context         optional {user_id, task_id}; informational, never proof of identity
//	summary         optional one line for people, at most 500 characters
//	payload         a JSON object whose shape belongs to the operation; required on a request
//	error           {code, message, retryable}; required on type error and not allowed otherwise
//
// A response, event or error carries the correlation_id and operation of the
// request it answers, and is addressed to the app that sent it. The error codes
// are invalid_message, unknown_operation, invalid_payload, not_found, too_large,
// timeout, unavailable and internal. An encoded message is at most
// MaxEncodedBytes (512 KiB). from and to say who sent a message and who should
// read it, but nothing authenticates them, and a payload carries no secrets.
//
// # Example: a request
//
// The assistant asks knowledge for documents:
//
//	{
//	  "schema_version": 1,
//	  "id": "3f0c1c52-8a43-4c1e-9d0e-6a1b2f7d9e10",
//	  "type": "request",
//	  "correlation_id": "3f0c1c52-8a43-4c1e-9d0e-6a1b2f7d9e10",
//	  "from": "assistant",
//	  "to": "knowledge",
//	  "created_at": "2026-10-23T09:15:00Z",
//	  "operation": "documents_search",
//	  "context": {"user_id": "u-42", "task_id": "t-1001"},
//	  "summary": "Find documents about kubernetes probes",
//	  "payload": {"query": "kubernetes liveness probe", "tags": ["ops"], "limit": 5, "offset": 0}
//	}
//
// # Example: the response
//
// Knowledge answers with a new id and the same correlation_id:
//
//	{
//	  "schema_version": 1,
//	  "id": "b7a1e0d4-5c2f-4f6a-8b13-0d9e4c7a2f55",
//	  "type": "response",
//	  "correlation_id": "3f0c1c52-8a43-4c1e-9d0e-6a1b2f7d9e10",
//	  "from": "knowledge",
//	  "to": "assistant",
//	  "created_at": "2026-10-23T09:15:01.250Z",
//	  "operation": "documents_search",
//	  "context": {"user_id": "u-42", "task_id": "t-1001"},
//	  "payload": {"documents": [{"id": "d-17", "title": "Probes", "summary": "Liveness and readiness probes.", "tags": ["ops"], "score": 0.82, "url": "https://wiki.example/probes"}]}
//	}
//
// # Using the package
//
// NewRequest, Reply, Event and ErrorReply build messages; Encode and Decode
// move them to and from bytes (Decode checks the size before it parses and
// rejects unknown fields and trailing data); Validate checks the rules and, given
// a Registry, the payload against the schema of its operation; Render describes a
// message in one line: "request documents_search assistant->knowledge [3f0c1c52]
// summary: ... payload keys: limit, offset, query, tags". EnvelopeSchema returns
// the JSON Schema of the envelope for tools outside Go. Decode, Encode, Validate
// and the Registry return *Error values whose Code is one of the codes above and
// whose Message is fixed and safe to show to a user.
package message

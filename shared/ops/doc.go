// Package ops is the catalogue of the operations the apps ask of each other over
// the message envelope (package message): the name of each operation, the Go
// types of its request and response payloads, and the JSON Schemas (draft 2020-12)
// of both. The apps that answer and the app that asks share this one definition,
// so they cannot drift apart. The transport is package bus; the envelope is
// package message.
//
// # The operations
//
//	operation         answered by                  request                          response
//	documents_search  knowledge                    DocumentsSearchRequest           DocumentsSearchResponse
//	url_ingest        knowledge (phraseforge: not yet)  URLIngestRequest            URLIngestResponse
//	task_status       knowledge and phraseforge    TaskStatusRequest                TaskStatusResponse
//
// The assistant is the client of all three. A responder registers only what it
// answers; the client registers everything:
//
//	reg := message.NewRegistry()
//	err := ops.Register(reg, ops.URLIngest, ops.TaskStatus) // a responder
//	err = ops.Register(message.NewRegistry())               // the client: all of them
//
// Every payload is a JSON object with snake_case keys and only the documented
// fields (additionalProperties is false in every schema). Schemas(name) returns
// the two schemas for tools outside Go. The Parse functions (ParseDocumentsSearchRequest
// and the five like it) read a payload strictly into the typed value and return
// a *message.Error with the code invalid_payload when it does not fit. To build a
// payload, pass the typed value to message.NewRequest or message.Reply.
//
// # documents_search
//
// Request: query (1 to 2000 characters), tags (at most 20, each 1 to 64
// characters), start and end (RFC 3339 times in UTC ending in Z), limit (1 to 20;
// the responder uses 5 when it is left out), offset (0 or more). Response:
// documents (at most 20 of {id, title, summary, tags, score, url}; url is the
// link as knowledge's chat produces it, and may be relative) and total (optional).
//
//	{"query": "kubernetes liveness probe", "tags": ["ops"], "limit": 5}
//	{"documents": [{"id": "d-17", "title": "Probes", "summary": "Liveness and readiness probes.", "tags": ["ops"], "score": 0.82, "url": "/documents/d-17"}], "total": 1}
//
// # url_ingest
//
// Request: url (http or https, at most 2048 characters) and tags as above.
// Response: task_id and status, the state the app reports right after it accepted
// the work (for example queued). The responder persists the work before it
// answers.
//
//	{"url": "https://wiki.example/probes", "tags": ["ops"]}
//	{"task_id": "t-1001", "status": "queued"}
//
// # task_status
//
// Request: id (1 to 128 characters). Response: id, kind and status; step,
// progress (only where the app has one) and error (the app's own text, at most
// 1000 characters, left out when there is none); created_at and updated_at as
// RFC 3339 times in UTC ending in Z.
//
//	{"id": "t-1001"}
//	{"id": "t-1001", "kind": "url_ingest", "status": "running", "step": "fetch", "created_at": "2026-10-23T09:15:00Z", "updated_at": "2026-10-23T09:15:30Z"}
//
// # Rules
//
// A payload carries no secrets: no tokens, keys or passwords, and no credentials
// inside a url. task_status never returns a task's payload or result, because
// they can hold user text; TaskStatusResponse has no field for them and its
// schema forbids additional properties.
//
// In the Go types an optional field left out and one holding its zero value read
// alike, so "limit": 0 or "start": "" is refused by the schema (message.Validate
// with the registry) and not by the Parse functions.
package ops

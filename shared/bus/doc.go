// Package bus carries the envelopes of package message between the apps over
// NATS: a Responder answers the requests sent to an app, a Client sends a request
// to another app and waits for its one answer. It adds no wire format of its own;
// what travels is exactly an encoded message.Envelope.
//
// # Subjects
//
// Each app listens on one subject, dp.<app>.request (RequestSubject); <app> is
// an app name as message defines it. A Responder joins the queue group <app> on
// it, so replicas of one app share the work and each request reaches one of them.
// A Client sends the request with the NATS request/reply API, so the answer comes
// back on the requester's own reply inbox: there is no per-app reply subject.
//
// # What a Responder does with a message
//
// Everything between the received bytes and the reply bytes is one function,
// Responder.process, that needs no NATS connection:
//
//	bytes that do not decode, or break the envelope rules   invalid_message
//	bytes larger than message.MaxEncodedBytes               too_large
//	a message whose type is not request                     invalid_message
//	an operation the registry or the Responder does not know unknown_operation
//	a payload that breaks the operation's request schema    invalid_payload
//	all handler slots taken                                 unavailable (retryable)
//	a handler error that is a *message.Error                that code and message
//	any other handler error, or a handler panic             internal
//	a response payload that breaks its response schema      internal
//
// The message of an internal error is always "the request could not be
// processed": a Go error or a panic text never leaves the app. They are logged
// instead, with the operation and the message id (a bounded error text, never
// the payload). At most 8 handlers run at once (WithMaxConcurrent); a request
// that finds no free slot is answered at once, never queued, so the NATS callback
// never blocks. A handler's context ends when its request outlives
// WithRequestTimeout (25 s) or the Responder is stopped; a handler must watch it.
//
// # What a Client returns
//
// Every error Request returns is a *message.Error with one of the fixed codes.
// An error message from the other app comes back as its code and message; no
// responders on the subject is unavailable, no answer in time is timeout, and an
// answer that is not a valid envelope for this request is invalid_message. The
// wire flag error.retryable is a function of the code (unavailable and timeout
// are retryable, the others are not); Retryable(err) reads it.
//
// # Why core request/reply and not JetStream
//
// An early draft of the spec had a JetStream durable consumer for the requests
// that must survive a restart (url_ingest). This package does not use JetStream:
// a long operation is saved by the receiving app in its own job table before it
// answers, and the answer to the requester is the saved task id. The requester
// needs that answer at once, and a JetStream consumer cannot give it: it only
// delivers the request later. Once the task is saved, the app's own job table
// is what survives a restart. While an app is down the requester gets
// unavailable (retryable) and asks again.
//
// # Unauthenticated
//
// NATS in the dev cluster has no authentication and from and to are not proof
// of identity (see package message). Apps listen only when a NATS URL is
// configured (Enabled).
package bus

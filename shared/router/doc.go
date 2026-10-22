// Package router routes a model call to one of several configured models: the
// highest-priority available model that has the tags the call needs, falling
// back to lower ones when a model fails. A circuit breaker, not pings, decides
// what is available.
//
// # Embeddings
//
// Router.Embed returns the embedding of one text in one embedding space (the
// embedding_space of a model in modelcfg). It shares selection, the breaker,
// attempts and logging with Complete, but only models of that space are
// candidates, highest priority first, so a fallback is only ever another copy of
// the same model, never a model whose vectors mean something else. A vector whose
// length is not the model's dimension is a failed attempt and is never returned.
// An Attempt of an embedding call has Kind AttemptEmbed and the tag
// "embedding_space=<space>"; its OutputTokens are zero. A provider takes part by
// implementing EmbeddingProvider, which ollama and openai do.
//
// # Troubleshooting
//
// Every call to Complete or Embed that gets as far as choosing a model writes log lines
// through the router's slog logger (WithLogger). All lines of one call share a
// call_id (8 hex characters) and a "tags" array, and carry whatever
// WithLogAttrs adds (the assistant adds task_id and user_id). A line never
// holds a prompt, a reply, a schema, a request body or an API key; the "error"
// field is the provider's own error text, cut to 500 bytes.
//
// Messages, levels and fields:
//
//	message             level   fields (besides call_id, tags and the WithLogAttrs ones)
//	llm candidates      debug   candidates (model ids in try order), skipped ([{model_id, reason, available_again}];
//	                            reason is model_unavailable, provider_unavailable or missing_credentials;
//	                            available_again is left out for missing_credentials)
//	llm no model        warn    models_checked (every configured model id)
//	llm attempt         info    attempt (1-based), of (candidate count), model_id, provider_id, provider_type,
//	                    (ok,    model_name, outcome (ok|error|timeout|rate_limited|canceled), failure_kind (failures
//	                    canceled) only), duration_ms, input_tokens, output_tokens, http_status and request_id (only
//	                    warn    when known), error (failures only, bounded)
//	                    (failed)
//	llm breaker open    warn    model_id, provider_id, reason (failure kind), consecutive_failures, cooldown_seconds,
//	                            available_again (RFC 3339, UTC)
//	llm provider down   warn    provider_id, models (ids), cooldown_seconds, available_again
//	llm breaker closed  info    model_id, previous_consecutive_failures
//	llm call done       info    outcome (ok|failed|canceled), attempts, model_id (winner, ok only), total_duration_ms,
//	                    (warn   fell_back (the answer came from a lower candidate, or a failed/canceled call tried
//	                    when    more than one model), chain (failed only: ["model_id:failure_kind", ...]; a candidate
//	                    failed) skipped because its provider went down mid-call shows as "model_id:skipped")
//
// Two more lines are not part of a call, so they have no call_id or tags: they
// report a provider that WithAvailability found unusable, once when that starts
// (also at construction, so the operator sees it at startup) and once when it
// ends, never per call:
//
//	llm provider unavailable  warn  provider_id, reason (missing_credentials), models (ids),
//	                                credential_source ("env:NAME", "file:/path" or "none": the provider's api_key_from,
//	                                followed by "; header NAME from env:NAME" for each header read from the environment or a file)
//	llm provider available    info  provider_id, models (ids), credential_source
//
// The credential_source names variables and files, never a credential or a header value.
//
// A call refused by the call check, or made with an already ended context,
// leaves no lines. "llm call done" is written once per call, last.
//
// Examples, with the logs as JSON lines on stdout:
//
//	# follow one call
//	jq -c 'select(.call_id == "3fa9c1d2")' logs.jsonl
//
//	# every failed attempt of one model, with what the provider said
//	jq -c 'select(.msg == "llm attempt" and .model_id == "big-gpu" and .outcome != "ok") | {time, call_id, failure_kind, http_status, request_id, error}' logs.jsonl
//
//	# calls that fell back, and the chain they went through
//	jq -c 'select(.msg == "llm call done" and .fell_back == true)' logs.jsonl
//
//	# when models were taken out of service, and until when
//	grep -E '"msg":"llm (breaker open|provider down|breaker closed)"' logs.jsonl
//
// The request_id is the provider's own id for the call; quote it, with the
// time, in a support request.
package router

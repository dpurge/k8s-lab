---
title: Retry transient LLM errors (5xx, connection, stream, idle timeout)
kind: bugfix
status: done
version: 1
updated: 2026-10-02
branch: main
---

## Problem / Motivation

In production (jdpct101, 43h logs), 6 of 62 translation LLM calls failed with
outcome=error and limit="" within ~1.3s, consistent with 5xx or connection
errors (cause unknown from logs). Any such error fails the job immediately; the
only recovery path is a manual Retry from the Jobs page. Transient errors
(HTTP 5xx, connection failures, stream ended early, idle-stall timeouts) are
recoverable and should be retried up to 3 times, drawing from the same
validation-retry budget introduced in spec `phraseforge-item-correction-retry-budget`.
First-token and overall timeouts are not retried (would cost another multi-minute wait).

## Acceptance Criteria

- [x] New pure function `llm.IsRetryable(err error) bool` in `shared/llm/llm.go` returns true for HTTP 5xx, stream-ended-early, stream error field, idle-stall `TimeoutError`, and connection-level errors (`*url.Error`, `net.Error` that are not Timeout() and not wrapping context.Canceled/DeadlineExceeded); false for 4xx, first_token, overall, context errors, nil.
- [x] `shared/llm/llm.go`: add `type StatusError struct{StatusCode int; Status string; Body []byte}` with Error() returning "chat provider returned <Status>: <Body>"; `do()` returns it for non-2xx. Add sentinel `var ErrStreamIncomplete` wrapping both stream error sites with an `Is` method so error text stays unchanged.
- [x] `phraseforge/internal/ai/retry.go:runWithCorrection` signature gains injected `sleep func(context.Context, time.Duration) error`; on retryable call error with attempts remaining, sleeps 5s then 15s (then 15s each repeat), retries with same history (no correction turn); non-retryable call error returns immediately; exhausted budget returns `after N attempts: %w`.
- [x] `phraseforge/internal/ai/ai.go:Generate` (process_text, process_dialog, title, translation, transcription, generate_*) uses `runWithCorrection` with transient retry (validate=nil), drawing `maxAttempts` from the purpose's config. `HandleItemTranslation` also uses `runWithCorrection` but with a validate function that checks the structured reply; validation and transient errors share the same attempt budget.
- [x] Tests: `shared/llm/retryable_test.go` classifies errors from real httptest calls (500, 503, 400, 404, stream error chunk, stream ended early, idle stall, first_token, overall, connection refused), caller cancel, and direct cases (nil, plain, context errors, ErrUnexpectedEOF, wrapped 502); TestErrorTextUnchanged pins the exact three messages. `phraseforge/internal/ai/retry_transient_test.go` (8 tests) covers: 5xx then ok with 5s backoff; 5xx x3 exhausting budget with [5s,15s] waits; non-retryable errors (first_token, overall, 4xx) make 1 call; transient and validation share budget; cancel during backoff; defaultSleep; backoffAfter. `phraseforge/internal/ai/itemcorrection_test.go` (4 end-to-end tests) via httptest fake Ollama: 500 then ok for callWithRetry; 3x500 with 3 requests and 2 waits then "after 3 attempts"; 400 is not retried; item job with 500 then phrase mismatch then success.
- [x] `cd shared && go test ./llm/...` and `cd phraseforge && go test ./...` pass; knowledge tests unaffected (knowledge/CHANGELOG.md empty in git status, no knowledge code changed).

## Approach

1. `shared/llm/llm.go`: add `StatusError` struct (StatusCode, Status, Body) and `ErrStreamIncomplete` sentinel; wrap both stream errors with %w; return StatusError in `do()` for non-2xx; pure `IsRetryable(err)` function.
2. `phraseforge/internal/ai/retry.go`: extend `runWithCorrection` with injected sleep; on retryable error with attempts remaining, backoff (5s / 15s / 15s...) and retry with same messages; exhausted budget returns wrapped error.
3. `phraseforge/internal/ai/ai.go:Generate` calls `runWithCorrection` with validate=nil (transient only), passing purpose's MaxAttempts via effectiveMaxAttempts.
4. Error strings unchanged; knowledge app (never calls IsRetryable) unaffected.

## Affected Areas

- `shared/llm/llm.go`
- `shared/llm/llm_test.go`
- `phraseforge/internal/ai/retry.go`
- `phraseforge/internal/ai/retry_test.go`
- `phraseforge/internal/ai/ai.go`
- `phraseforge/internal/ai/ai_test.go` (or new test file)

## Out of Scope

- Retrying first_token / overall timeouts.
- Jitter; per-error-type budgets; job-level retry counters / auto re-enqueue.
- Knowledge app opting in; configurable backoff.
- Ingest cap / chunking (spec `phraseforge-long-text-ingest`).
- Circuit breaker.

## Implementation Notes

1. `shared/llm/llm.go`: Added `StatusError` struct (StatusCode, Status, Body) with Error() method returning "chat provider returned <Status>: <Body>" — identical text to before. Added sentinel `ErrStreamIncomplete` and internal `streamError` wrapper type with an `Is` method so both stream error sites (line 270, 275) wrap their original error text unchanged. `do()` method (line 358) now returns StatusError for non-2xx status codes. Pure `IsRetryable(err)` function (lines 135-155) classifies errors: true for 5xx StatusError, ErrStreamIncomplete, idle TimeoutError, io.ErrUnexpectedEOF, and net.Error that is not Timeout(); false for nil, 4xx, first_token/overall timeouts, context.Canceled/DeadlineExceeded, plain errors.

2. `shared/llm/retryable_test.go`: New test file validating IsRetryable against real httptest servers (500, 503 -> true; 400, 404 -> false; stream error chunk, stream ended early, idle stall -> true; first_token, overall -> false; connection refused -> true; caller cancel -> false). TestErrorTextUnchanged pins the three error messages callers have always seen.

3. `phraseforge/internal/ai/retry.go`: `runWithCorrection` signature (line 69-76) now takes injected `sleep sleepFunc` parameter; validate may be nil. Backoff logic (lines 90-94): on retryable error with attempts remaining, calls sleep with backoffAfter(attempt) — 5s for first failure, 15s for second and later — then retries with same message history (no correction turn added). Non-retryable errors return immediately (line 87). Exhausted budget wraps the last error as "after N attempts: %w" (line 108). `backoffAfter(n)` function (lines 31-37) returns 5s then 15s per attempt count. `defaultSleep` (lines 43-52) uses timer + ctx.Done for cancellation support. `Service.sleep` field (line 173) allows injection; `sleeper()` method (lines 112-117) returns s.sleep or defaultSleep.

4. `phraseforge/internal/ai/ai.go`: `callWithRetry` function (lines 322-331) extracts the call-with-retry pattern so it is testable without a DB. `Generate` (line 316) calls callWithRetry with validate=nil, accepting any reply. `callLLM` (lines 338-370) logs attempt/maxAttempts in telemetry (line 368).

5. `phraseforge/internal/ai/itemjob.go`: `callItemWithCorrection` (lines 102-119) calls `runWithCorrection` with a validate function (lines 110-117) that checks the structured reply via `decideItemWrites`. Both transient and validation errors share the maxAttempts budget.

6. `phraseforge/internal/ai/retry_transient_test.go`: 8 tests covering transient retry: TestTransientErrorRetriesAfterBackoff (5xx then ok, 5s backoff); TestTransientErrorExhaustsBudget (3x5xx, [5s,15s] waits); TestNonRetryableCallErrorsAreNotRetried (first_token/overall/4xx), TestTransientAndValidationShareOneBudget (both error types, shared budget), TestNilValidateAcceptsAnyReply, TestCancelDuringBackoffStopsRetrying (interrupt wait on cancel), TestDefaultSleepWaitsAndHonoursCancel, TestBackoffAfter.

7. `phraseforge/internal/ai/itemcorrection_test.go`: 4 end-to-end tests via httptest fake Ollama: TestCallItemWithCorrectionFixesMismatchedPhrase (validation rejects, then accepts); TestCallItemWithCorrectionGivesUpAfterBudget (validation fails 3x); TestCallWithRetryRecoversFromServerError (500 then 200 with 5s backoff); TestCallWithRetryGivesUpAfterBudget (500 x3, 2 waits); TestCallWithRetryDoesNotRetryClientErrors (400, 1 call no wait); TestItemCallSharesBudgetAcrossServerErrorAndBadPhrase (500 then validation failure then success, messages [1 1 3]).

All files verified with `go vet ./...` and `go test ./...` passing in shared and phraseforge; gofmt clean. Knowledge unaffected (never calls IsRetryable).

## Validation

Baseline before changes: `cd shared && go test ./llm/...`, `cd knowledge && go test ./...`, `cd phraseforge && go test ./...` all pass.

After implementation:
- `cd shared && go vet ./...` clean (added shared/llm/retryable_test.go)
- `cd shared && go test ./...` pass (all llm tests including new IsRetryable tests and TestErrorTextUnchanged)
- `cd phraseforge && go vet ./...` clean
- `cd phraseforge && go test ./...` pass (all ai tests including retry_transient_test.go and itemcorrection_test.go)
- `gofmt -l shared/llm/llm.go shared/llm/retryable_test.go phraseforge/internal/ai/retry.go phraseforge/internal/ai/retry_transient_test.go phraseforge/internal/ai/ai.go phraseforge/internal/ai/itemjob.go phraseforge/internal/ai/itemcorrection_test.go` — no files flagged
- `cd knowledge && go test ./...` pass (unaffected — no knowledge code changed, `git status --short knowledge` is empty)

Known limitations:
- Behavior against real prod Ollama not verified; after user deploys, check `llm call` log lines with attempt>1 and compare error rate vs. pre-deployment (6 of 62 failures in 43h before, to be re-measured)
- 5s/15s backoff duration not tuned for cold model load; unknown if sufficient for first Ollama startup on a given node

## Documentation Review

Checked: phraseforge/CHANGELOG.md, phraseforge/README.md lines 30-49 (LLM configuration section), knowledge/CHANGELOG.md, knowledge/README.md, specs/tech-stack.md Artifacts table (shared/llm consumed by phraseforge and knowledge).

Findings:

1. **phraseforge/CHANGELOG.md Fixed section — entry ready to add**: The Unreleased section already has a `### Fixed` section (line 95+), so the entry for transient retry belongs there (not requiring a new section to be created).
   - Evidence: phraseforge/CHANGELOG.md:95 shows existing Fixed section.
   - Entry: "An LLM call that fails with a transient error (HTTP 5xx, a connection error, a stream that ends early, or an idle stall) is now retried within the same `maxAttempts` budget (default 3), waiting 5s and then 15s; first-token and overall timeouts are not retried." (this entry is already in the file at line 97-99).

2. **phraseforge/README.md line 38 — already updated**: The `<purpose>.maxAttempts` bullet has been rewritten to explicitly mention that the budget is shared between validation rejects and transient failures, and that first-token/overall timeouts are not retried.
   - Evidence: phraseforge/README.md:38 already states "shared between replies that fail validation... and transient failures (HTTP 5xx, connection errors, a stream that ends early, idle stalls)...".

3. **Knowledge app unaffected — no changelog entry needed**: knowledge/CHANGELOG.md is not affected. shared/llm/llm.go is compiled into knowledge but knowledge never calls IsRetryable; the shared code change is only used by phraseforge. Per specs/tech-stack.md Artifacts table, shared/llm feeds both phraseforge and knowledge, but because knowledge does not invoke the new IsRetryable function and produces no user-facing error behavior change, it requires no changelog entry.

4. **No constitution drift**: specs/tech-stack.md Artifacts table already lists shared/llm as a built-from dependency; no changes needed. specs/roadmap.md: this feature is in Now and will be removed as part of this documentation phase. specs/mission.md: no change needed.

## Documentation Updates

Documentation changes applied:

- **phraseforge/CHANGELOG.md** — `### Fixed` section (already present): entry added — "An LLM call that fails with a transient error (HTTP 5xx, a connection error, a stream that ends early, or an idle stall) is now retried within the same `maxAttempts` budget (default 3), waiting 5s and then 15s; first-token and overall timeouts are not retried."
- **phraseforge/README.md** — line 38: `<purpose>.maxAttempts` bullet rewritten to clarify the budget is shared between validation rejects (reply + error sent back to model) and transient failures (HTTP 5xx, connection errors, a stream that ends early, idle stalls); first-token and overall timeouts are not retried.
- **knowledge/CHANGELOG.md** — no change (knowledge does not call IsRetryable; error text unchanged; knowledge builds and its tests pass; no knowledge file changed).
- **phraseforge/k8s/configmap.yaml** — no change.
- **Constitution files** (specs/tech-stack.md, specs/roadmap.md, specs/mission.md) — no change.

Validation: `go test ./internal/config/` passes after the docs edits.

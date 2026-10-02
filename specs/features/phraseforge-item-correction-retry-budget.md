---
title: Retry failed item validations with error feedback to the LLM
kind: bugfix
status: done
version: 1
updated: 2026-10-02
branch: main
---

## Problem / Motivation

In production (jdpct101, last 43 hours of logs), 70 of 72 `generate_models_item`
jobs ended with `outcome=error`, but their LLM calls reported `outcome=success`.
The failures occur during output validation after the call succeeds: either the
JSON is invalid (`phraseforge/internal/ai/itemresponse.go:decodeItemResponse`)
or the model echoed back a phrase that does not match the stored item's phrase
after TrimSpace and NFC normalization (`checkRequiredAndPhrase`). Currently,
any validation error fails the job immediately; the only recovery path is a
manual Retry from the Jobs page. The validation error is never sent back to the
model for correction.

## Acceptance Criteria

- [ ] New pure function `runWithCorrection(ctx, maxAttempts, call, validate, msgs)` in `phraseforge/internal/ai/retry.go` loops up to `maxAttempts` attempts: call error returns immediately; validation error appends the error to the message history and retries; exhausted budget returns last validation error wrapped as `after N attempts: %w`. maxAttempts <= 0 defaults to 3.
- [ ] Correction text is a code constant with named placeholder `{{error}}`: `Your previous reply was rejected: {{error}}\nReturn only the corrected JSON object.`
- [ ] `phraseforge/internal/ai/itemjob.go:HandleItemTranslation` uses `runWithCorrection` with call=per-attempt `s.callLLM` and validate=`func(raw) { _, err := decideItemWrites(...); return err }`.
- [ ] `phraseforge/internal/ai/ai.go:callLLM` log line gains `attempt` and `max_attempts` attributes; a reply failing validation is logged with `outcome=invalid_reply`.
- [ ] `phraseforge/internal/config.PurposeConfig` and `purposeFileConfig` (field-identical pairs) gain `MaxAttempts int` (yaml `maxAttempts`); default 3 for every purpose in `defaultFileConfig()`.
- [ ] Tests: `phraseforge/internal/ai/retry_test.go` (unit tests for `runWithCorrection`: first-try success, invalid-then-valid, always-invalid returns exactly 3 calls, maxAttempts 0 defaults to 3, call error returns immediately, ctx cancelled stops). One end-to-end test using httptest fake Ollama. `config_test.go` extended for MaxAttempts defaults and overrides.
- [ ] `go test ./internal/ai/... ./internal/config/...` passes in phraseforge.

## Approach

1. New `phraseforge/internal/ai/retry.go`: `runWithCorrection` signature and implementation as specified above — loop attempt 1..maxAttempts, check `ctx.Err()` first each iteration, call error returns immediately (transient retry is separate), validation error appends messages and loops, exhausted budget returns wrapped final error.
2. Correction text is constant using named placeholder `{{error}}`, interpolated at call time.
3. `phraseforge/internal/ai/itemjob.go`: replace direct `decideItemWrites` validation with `runWithCorrection` wrapper, passing `callLLM` and the validation function.
4. `phraseforge/internal/ai/ai.go`: extend `callLLM` log line with `attempt` and `max_attempts` attributes; log `outcome=invalid_reply` for validation-failed replies.
5. `phraseforge/internal/config/config.go`: add `MaxAttempts int` to both `PurposeConfig` and `purposeFileConfig` (kept field-identical); set default 3 in `defaultFileConfig()`.
6. Configuration: one shared budget of 3 total attempts per logical call; shared between validation retries and any future transient-retry mechanism.

## Affected Areas

- `phraseforge/internal/ai/retry.go` (new)
- `phraseforge/internal/ai/retry_test.go` (new)
- `phraseforge/internal/ai/itemjob.go`
- `phraseforge/internal/ai/ai.go`
- `phraseforge/internal/config/config.go`
- `phraseforge/internal/config/config_test.go`

## Out of Scope

- Transient retry of 5xx / connection / idle timeouts (spec `phraseforge-llm-transient-retry`).
- Any change to `shared/llm`.
- Correction of `ai.Service.Generate` (process_text, title, translation — no validation loop there).
- Ingest cap and chunking (spec `phraseforge-long-text-ingest`).
- Knowledge app; auto-repair of phrase in code; per-chunk checkpoints; follow-up jobs' NumCtx.

## Implementation Notes

1. Config: `MaxAttempts` added to `config.PurposeConfig` and `purposeFileConfig` (YAML field `maxAttempts`); default 3 applied to all 9 purposes in `defaultFileConfig()` (phraseforge/internal/config/config.go:137–171).
2. Retry loop: new `phraseforge/internal/ai/retry.go` with `defaultMaxAttempts=3`, `effectiveMaxAttempts(n)`, `correctionTemplate` constant containing `{{error}}` placeholder, and `runWithCorrection(ctx, maxAttempts, call, validate, msgs)` function.
3. Retry tests: new `phraseforge/internal/ai/retry_test.go` with 7 unit tests (first-try success, invalid reply then valid success, exhausted 3 attempts, maxAttempts 0/-1 treated as 3, maxAttempts 1 with no retry, call error returned immediately, cancelled context stops loop).
4. LLM call logging: `ai.go:callLLM` gained `attempt, maxAttempts int` parameters; 'llm call' log line adds `attempt` and `max_attempts` attributes; `Generate` calls pass `attempt=1, maxAttempts=1`.
5. Item validation with retry: new method `itemjob.go:callItemWithCorrection(ctx, p, prompt, def, schema, msgs, sections)` wraps `callLLM` in `runWithCorrection`; `HandleItemTranslation` calls it. Validation-failed replies logged on separate line as `llm reply rejected` (outcome, attempt, max_attempts, but no content, since error text embeds the model's reply).
6. End-to-end tests: new `phraseforge/internal/ai/itemcorrection_test.go` with two tests using httptest fake Ollama: wrong phrase then correct phrase succeeds on request 2; three bad replies exhausts 3 attempts, each request includes the exact validation error in the corrected message.

Status: implementing.

## Validation

**Build and test:** Baseline tests before changes passed. After changes:
- `gofmt -l phraseforge/...` — no unformatted files
- `go vet ./...` — no issues in phraseforge
- `go test ./...` — all packages passed (ai, config, generate, ingest, pagination, server)

**Test coverage:** Unit tests cover `runWithCorrection` loop control (attempt counting, error return, context cancellation, maxAttempts defaults). Integration tests cover item validation retry with a fake HTTP Ollama. Config tests verify MaxAttempts defaults and file override.

**Gap:** Production behavior against the real model has not been verified. Post-deployment, verify by comparing `generate_models_item` job failure rates in prod logs (baseline: 70 of 72 failures in 43h before change) and sampling job error text from the database. Actual phrase-mismatch error strings in prod are not yet known.

**Known limitation:** On CPU-only nodes, a job exhausting all 3 attempts can run 3x longer than a single attempt; first-token and overall timeouts are not retried by design.

Status: validating.

## Documentation Review

**Changelogs:**
- `phraseforge/CHANGELOG.md` (path:5) has `## [Unreleased]` section. This change adds config key `maxAttempts` and fixes validation-failed jobs by retrying with error feedback. Proposed entry in `### Fixed`: "Item validation failures (JSON decode, phrase mismatch) now retry with error feedback to the LLM, up to a configurable budget (default 3 attempts per item), instead of failing immediately."

- `CHANGELOG.md` (root): per tech-stack.md Artifacts table (line 63), root is "environment" artifact with versioning "none" — no changelog entry required.

**Configuration docs:**
- `phraseforge/README.md` LLM configuration section (line 30) documents per-purpose defaults but does not list `maxAttempts`. Should note that maxAttempts is configurable per purpose with a default of 3 and ≤0 means default.
- `phraseforge/k8s/configmap.yaml` (path:7-15) declares "Every purpose's provider/model/timeoutSeconds/prompt is declared explicitly here" but currently lacks `maxAttempts` for all 9 purposes. All 9 should be added: `maxAttempts: 3` (transcription, translation, title, processText, processDialog, generateVocabulary, generateModels, vocabularyItem, modelsItem).

**Constitution files:**
- `specs/tech-stack.md` — no drift detected (artifacts table and conventions remain current).
- `specs/mission.md` — no drift detected (problem/users/value/non-goals remain current).

Status: documenting.

## Documentation Updates

- **phraseforge/CHANGELOG.md**: Added entry in `### Fixed` under `## [Unreleased]` documenting that item validation failures (JSON decode, phrase mismatch) now retry with error feedback to the LLM up to configurable `maxAttempts` (default 3) instead of failing immediately.
- **phraseforge/README.md**: Added bullet point documenting `<purpose>.maxAttempts` (default 3) configuration option in the LLM configuration section, after the `timeoutSeconds` bullet.
- **phraseforge/k8s/configmap.yaml**: Added `maxAttempts: 3` configuration to all 9 purposes (transcription, translation, title, processText, processDialog, generateVocabulary, generateModels, vocabularyItem, modelsItem); updated header comment to list `maxAttempts` alongside `provider/model/timeoutSeconds`.
- **Config test validation**: `go test ./internal/config/` passes with all 8 tests passing, confirming defaults and overrides work correctly.

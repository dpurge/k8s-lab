---
title: Move knowledge prompt templates into the mounted ConfigMap
kind: feature
status: done
version: 3
updated: 2026-09-22
branch: main
---

## Problem / Motivation

This is the `knowledge-prompts-configmap` item from `specs/roadmap.md`'s `## Next` list: move model prompt templates (chat/generate/translate system prompts) from Go source into the mounted ConfigMap YAML, so prompts are editable without a rebuild. It follows the same pattern already established by the `knowledge-config-configmap` feature (done), which moved all other knowledge-app settings (models, endpoints, thresholds) into `knowledge/k8s/configmap.yaml`, read once at startup via `knowledge/internal/config`. Today four prompts remain hardcoded in Go: `knowledge/internal/chat`'s system prompt (embedded from `prompt.md`), `knowledge/internal/generate`'s title and summary prompts (package-level consts), and `knowledge/internal/translate`'s prompt (built at call time via `fmt.Sprintf` with the configured knowledge-base language substituted twice).

Per user decision, the translate prompt's dynamic language substitution will use a `{{language}}` token in the stored template, replaced via a single `strings.ReplaceAll` call that replaces all occurrences of `{{language}}` in one pass — chosen over keeping literal `%s` verbs, so someone editing the ConfigMap doesn't need to know Go's fmt verb syntax. Prompt text must use only the `{{varname}}` token syntax, never Go's `%s` or other fmt verbs.

As part of this consolidation, the translate model will be consolidated from `rinex20/translategemma3:12b` to `gemma4:12b`, unifying the app's generative models to two: `bge-m3` (embeddings) and `gemma4:12b` (chat, generate, translate). This decision is based on live side-by-side testing against the local Ollama instance, translating the same French/German/Japanese/Spanish sentences using both models with the app's production system-prompt wording. Both models showed comparable quality; `rinex20/translategemma3:12b` was marginally more idiomatic on 2 of 4 samples (Japanese, Spanish), but neither made factual or accuracy errors, and the difference was not decisive for the app's use case. The consolidation is therefore justified by operational simplicity, accepting the marginal tradeoff.

## Acceptance Criteria

- [x] `knowledge/internal/config.Config` gains four new string fields — `ChatPrompt`, `GenerateTitlePrompt`, `GenerateSummaryPrompt`, `TranslatePrompt` — populated by `config.Load()` from the mounted YAML file under a new `prompts:` block (keys: `chat`, `generateTitle`, `generateSummary`, `translate`), defaulting in `defaultFileConfig()` to today's exact hardcoded text when the file omits them.
- [x] `knowledge/internal/chat`'s system prompt comes from `cfg.ChatPrompt` (the `Service` already stores `cfg`) instead of the `//go:embed prompt.md` package variable; `knowledge/internal/chat/prompt.md` and the now-unused `embed` import are removed.
- [x] `knowledge/internal/generate`'s `Service` stores `titlePrompt`/`summaryPrompt` fields set from `cfg` in `New()`, replacing the two package-level `const` prompts; `Title()`/`Summary()` use the stored fields.
- [x] `knowledge/internal/translate`'s `Service` stores a `promptTemplate` field (from `cfg.TranslatePrompt`) set in `New()`; `Translate()` substitutes all occurrences of `{{language}}` with `s.language` via a single `strings.ReplaceAll` call instead of the current `fmt.Sprintf` literal, producing byte-identical output to today for the same language.
- [x] `knowledge/k8s/configmap.yaml` gains a `prompts:` block under `data["config.yaml"]` carrying today's actual text for all four prompts verbatim (translate's using `{{language}}` in place of its two dynamic insertion points).
- [x] `translate.model` in `knowledge/k8s/configmap.yaml` changes from `rinex20/translategemma3:12b` to `gemma4:12b`; `translate.provider` (`ollama`) and `translate.baseURL` (`http://host.docker.internal:11434`, identical to chat's) remain unchanged; `defaultFileConfig()` in `knowledge/internal/config/config.go` updates the default for `f.Translate.Model` to match.
- [x] Behavior is unchanged: the same prompt text reaches the model as today, and translate output quality is maintained. Validated live (run the app, exercise chat, generate title/summary, and translate), not just by a successful build.
- [x] No changes to any other config field, `deployment.yaml`, `migrate-job.yaml`, or the Taskfile's `deploy-knowledge` ordering — `configmap.yaml` already exists and is already applied before the migrate Job runs; this only adds/updates keys to its existing YAML body.

## Approach

1. `knowledge/internal/config/config.go`: add a `Prompts` struct to `fileConfig` (yaml tags `chat`, `generateTitle`, `generateSummary`, `translate`) and matching flat fields on `Config` (`ChatPrompt`, `GenerateTitlePrompt`, `GenerateSummaryPrompt`, `TranslatePrompt`); set their exact current values as defaults in `defaultFileConfig()`; wire them through in `Load()`'s final `Config{}` construction.
2. `knowledge/internal/chat/chat.go`: remove the `//go:embed prompt.md` var and the resulting now-unused `_ "embed"` import; replace `systemPrompt` in `answer()` with `s.cfg.ChatPrompt`. Delete `knowledge/internal/chat/prompt.md`.
3. `knowledge/internal/generate/generate.go`: add `titlePrompt`/`summaryPrompt` fields to `Service`, set from `cfg` in `New()`; delete the `titlePrompt`/`summaryPrompt` package-level consts; use the struct fields in `Title()`/`Summary()`.
4. `knowledge/internal/translate/translate.go`: add a `promptTemplate` field to `Service`, set from `cfg.TranslatePrompt` in `New()`; in `Translate()`, replace the `fmt.Sprintf` call with a single `strings.ReplaceAll(s.promptTemplate, "{{language}}", s.language)` that replaces all occurrences of the placeholder in one call, replacing the now-unused `fmt` import if nothing else in the file needs it.
5. `knowledge/k8s/configmap.yaml`: add a `prompts:` block under `data.config.yaml` with today's exact text for `chat`/`generateTitle`/`generateSummary`/`translate`, using `{{language}}` for translate's two dynamic spots.
6. Build, then live-validate: run the app and exercise the chat, generate (title/summary), and translate endpoints, confirming output/behavior matches pre-change.
7. `knowledge/internal/config/config.go`: update `defaultFileConfig()`'s `f.Translate.Model` default from `rinex20/translategemma3:12b` to `gemma4:12b`.
8. `knowledge/k8s/configmap.yaml`: update the `translate.model` value from `rinex20/translategemma3:12b` to `gemma4:12b`. Note: the separate `translate:` config block (with `model`, `provider`, `baseURL`, `numCtx`) is deliberately kept, not merged or deduplicated with the `chat:` block. This matches the existing convention where `chat:` and `generate:` independently duplicate the same model/provider/baseURL values; `translate:` doing the same is not a new pattern and maintains architectural clarity (each service controls its own model/endpoint).

Risks: low — purely additive to an already-existing ConfigMap load path already exercised by `knowledge-config-configmap`, plus a straightforward model name change. Main risk is a transcription typo when moving prompt text, or an incorrect double-replace for translate; mitigated by diffing the moved text against the original Go source during implementation and by a unit test asserting both `{{language}}` occurrences are substituted.

Testing strategy: extend `knowledge/internal/translate/translate_test.go` with a case asserting both template occurrences of `{{language}}` are substituted correctly; live smoke-test the three model-calling paths per the Acceptance Criteria (no new automated coverage is feasible for `chat`/`generate` today since they have no existing test files and this change doesn't add test infrastructure beyond what's already there — note this as a known gap, not silently skip it).

## Affected Areas

- `knowledge/internal/config/config.go`
- `knowledge/internal/chat/chat.go`, `knowledge/internal/chat/prompt.md` (deleted)
- `knowledge/internal/generate/generate.go`
- `knowledge/internal/translate/translate.go`, `knowledge/internal/translate/translate_test.go`
- `knowledge/k8s/configmap.yaml`

## Out of Scope

- Any other config field, `deployment.yaml`, `migrate-job.yaml`, or Taskfile changes.
- Adding new prompts, or changing any prompt's wording or model-facing behavior.
- Removing, merging, or deduplicating the separate `translate:` config block with `chat:` or `generate:`; this change only updates the model name within the existing `translate:` structure.
- Changes to `translate.provider`, `translate.baseURL`, or other translate config fields (e.g. `numCtx`); this is a model-name-only change.
- The separate `knowledge-markdown-chat-render` roadmap item.

## Implementation Notes

1. `knowledge/internal/config/config.go`: added `ChatPrompt`, `GenerateTitlePrompt`, `GenerateSummaryPrompt`, and `TranslatePrompt` fields to `Config`, added a `Prompts` struct to `fileConfig` with matching keys, set their exact current hardcoded values as defaults in `defaultFileConfig()`, and wired them through `Load()`'s final `Config{}` construction.

2. `knowledge/internal/chat/chat.go`: removed the `//go:embed prompt.md` variable and the now-unused `_ "embed"` import; `answer()` now uses `s.cfg.ChatPrompt` instead of the embedded package variable. Deleted `knowledge/internal/chat/prompt.md`.

3. `knowledge/internal/generate/generate.go`: `Service` now stores `titlePrompt` and `summaryPrompt` fields, set from `cfg` in `New()`; deleted the two package-level const prompts (`titlePrompt` and `summaryPrompt`); `Title()` and `Summary()` now use the struct fields.

4. `knowledge/internal/translate/translate.go`: `Service` now stores `promptTemplate` field (from `cfg.TranslatePrompt`), set in `New()`; `Translate()` now calls `strings.ReplaceAll(s.promptTemplate, "{{language}}", s.language)` instead of `fmt.Sprintf`, replacing all occurrences of the placeholder in one pass; removed the now-unused `fmt` import.

5. `knowledge/k8s/configmap.yaml`: added a `prompts:` block under `data["config.yaml"]` with the four prompt keys (`chat`, `generateTitle`, `generateSummary`, `translate`), each containing today's exact hardcoded text verbatim. The `translate` prompt uses `{{language}}` token in place of its two dynamic insertion points.

6. Added `TestTranslateSubstitutesAllLanguageOccurrences` to `knowledge/internal/translate/translate_test.go` — a behavior test using `httptest.Server` to stub Ollama's `/api/chat` and assert the real `Translate()` call path substitutes both `{{language}}` occurrences correctly (chosen over directly testing `strings.ReplaceAll` in isolation, so the test exercises the real code path).

7. Implemented directly on `main` branch per the approved branch strategy.

8. Changed `knowledge/internal/config/config.go`'s `defaultFileConfig()` to set `f.Translate.Model` default to `gemma4:12b` instead of `rinex20/translategemma3:12b`.

9. Changed `knowledge/k8s/configmap.yaml`'s `translate.model` from `rinex20/translategemma3:12b` to `gemma4:12b`, completing the model consolidation.

## Validation

- `go build ./...`, `go vet ./...`, `gofmt -l .` (clean), and `go test ./...` all passed in `knowledge/` before and after the change, with no regressions. New test `TestTranslateSubstitutesAllLanguageOccurrences` passes.

- Live-deployed via `task deploy-knowledge` to the local k3d cluster (`.k3d/kubeconfig`, not the machine's default kubectl context). Pod started cleanly; logs showed config parsing succeeded with startup line confirming `embeddings=ollama/bge-m3 ... chat=ollama/gemma4:12b generate=ollama/gemma4:12b`.

- Live-exercised all three prompt paths against the deployed app: `POST /api/v1/knowledge/generate/title` and `/generate/summary` returned correct results; a chat message against a seeded knowledge item returned an accurate, sourced answer (confirming `ChatPrompt` reached the model correctly); `POST /api/v1/ingest/text` with French input produced a correctly-translated English draft (confirming the ConfigMap's `{{language}}`-templated translate prompt and the consolidated `gemma4:12b` translate model both work end-to-end).

- All test data created during live validation (a knowledge item, a chat, an ingest job, a draft) was deleted afterward.

- The `gemma4:12b` vs `rinex20/translategemma3:12b` model consolidation decision was validated with a separate live comparison (documented in `specs/memory.md`'s 2026-09-22T15:49:24Z `[decision]` entry) — both models were unloaded from Ollama between test runs, and all requests were sent one at a time (never in parallel), per explicit user instruction to conserve local machine memory. Both models showed comparable quality; `rinex20/translategemma3:12b` was marginally more idiomatic on 2 of 4 samples (Japanese, Spanish), but neither made factual or accuracy errors, and the difference was not decisive for the app's use case.

- Known gap: no automated test coverage for `chat`/`generate` beyond what already existed (neither package had test files before this feature); validated live instead.

## Documentation Review

`knowledge/README.md`'s Configuration section (the YAML config example, lines 106-143) and `CHANGELOG.md`'s `[Unreleased]` section were checked against this change. Drift found:

- `knowledge/README.md:141` — the example's `translate.model` still showed `rinex20/translategemma3:12b` instead of the new consolidated `gemma4:12b`, contradicting the actual default now in the code.
- `knowledge/README.md:141` comment — described the translate model as "translation-specialized" and said it was "used by the ingest pipeline to translate chunks", but no longer reflects that it is now the same model used for chat/generate.
- `knowledge/README.md` — had no `prompts:` block in the YAML example, omitting documentation of the four newly-configurable prompt keys (`chat`, `generateTitle`, `generateSummary`, `translate`).
- `CHANGELOG.md:14-16` — the translation-capability bullet listed the old default model name `rinex20/translategemma3:12b` instead of the new `gemma4:12b`.

Constitution files (`specs/mission.md`, `specs/tech-stack.md`, `specs/roadmap.md`) required no content changes beyond the routine roadmap `Now` → done transition (not itself a constitution decision change).

## Documentation Updates

- `knowledge/README.md` (lines 138-142): Updated the `translate:` block's `model` from `rinex20/translategemma3:12b` to `gemma4:12b`, changed the trailing comment from "translation-specialized; used by the ingest pipeline to translate chunks" to "same model used for chat/generate; chosen after live comparison showed comparable translation quality", and added a new `prompts:` block documenting the four prompt keys with brief comments explaining their purpose and the `{{varname}}` token syntax (e.g. `{{language}}` for translate).

- `CHANGELOG.md` (lines 14-16): Updated the translation-capability bullet's default model reference from `rinex20/translategemma3:12b` to `gemma4:12b`.

- `CHANGELOG.md` (added after the translation bullet): Added a new entry stating that chat/generate/translate prompt templates are now configurable via the mounted config file (`knowledge/k8s/configmap.yaml`'s `prompts:` block) instead of being hardcoded in Go, so they are editable without a rebuild.

- `specs/memory.md`: Already contains two entries (2026-09-22T15:49:24Z `[decision]` and 2026-09-22T15:49:25Z `[convention]`) recording the model-consolidation decision and the `{{varname}}` token-syntax convention for future ConfigMap prompt edits — no additional update needed.

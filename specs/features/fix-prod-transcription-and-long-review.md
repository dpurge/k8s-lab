---
title: Stop storing/showing transcription where not needed; review long texts in paragraph chunks
kind: bugfix
status: done
version: 3
updated: 2026-10-14
branch: main
---

## Problem / Motivation

Two problems seen on prod (jdpct101, 2026-10-14).

**1. Transcription on a language that does not need one.** Vocabulary list 54
(`ell`/`grek`) has a transcription on 23 of 23 items, although `ime_config` for
`ell`/`grek` has `needs_transcription = f` and `ell` has no
`language_llm_sections` row. 24 of the 55 `ell` phrases have one. Evidence:
read-only prod queries. Cause: `generate.syncVocabItems`/`syncModelsItems`
(`generate.go`) store whatever `[transcription]` the model emitted
(`ParseVocabularyLine`, `parse.go`) with no check of the language. The item
jobs already gate on the section (`itemjob.go:211`); the list generation does
not. The list tables (`vocabulary-app.js:395,421`, `models-app.js:361,380`)
render the Transcription column unconditionally, so the stray values show.
Generating them wasted model time and tokens.

**2. Review of a long text fails.** Job 53969591 (`review_text`, transcription
of text 95) failed with `too long to review in one call: 26556 characters, the
limit is 12000` (`review_text.go:217`). 27 prod texts exceed 12000 characters
(body + reviewed part). Text 95 has 69 body paragraphs and 68 transcription
paragraphs (split on blank lines), so with the chosen rule below it would still
fail, with a clearer message.

Assumption (not asked): the translation review has the same limit and gets the
same splitting, with each locale's translation aligned to the body.

## Acceptance Criteria

- [x] A generated vocabulary or models list for a language without a
  transcription section and with `needs_transcription = false` stores no
  transcription, whatever the model emitted. A language that needs one keeps it.
- [x] The Transcription column in the vocabulary and models list tables, and
  the Transcription field in their item forms, are hidden when the list's
  language does not need transcription.
- [x] A text or dialog review over the 12000-character limit is split into
  paragraph chunks (blank-line separated), each reviewed in its own call within
  the limit, and the chunk results are merged into one finding with the same
  shape as today (whole corrected text as the suggestion).
- [x] Splitting needs the source and the reviewed part to have the same
  paragraph count. If they differ the job fails with a message naming both
  counts. A single paragraph over the limit also fails with a clear message.
- [x] A review under the limit behaves exactly as before (one call).
- [x] Generated transcription and translation of a text or dialog keep the
  source's paragraph structure: the output has exactly as many blank-line
  separated paragraphs as the source. A chunk whose reply has a different
  paragraph count is redone one paragraph per call; a paragraph reply is
  collapsed to one paragraph (no blank lines). If a single paragraph still
  cannot be generated the job fails, never storing a misaligned result.
- [x] Title, vocabulary and models generation are unchanged.
- [x] Tests use a fake LLM only. No Ollama call is made.
- [x] Prod rows holding a transcription for a language that does not need one
  are cleaned only after the user approves the exact SQL.

## Approach

1. `internal/generate/generate.go`: in `HandleGenerateVocab` and
   `HandleGenerateModels`, after parsing, clear `Transcription` on candidates
   when `ai.Service.EffectiveSections(language).TranscriptionPrompt` is blank
   (the same rule `decideItemWrites` uses). A small helper, shared by both.
2. `internal/server/vocabulary.go` and `models.go`: add `needsTranscription`
   to the list detail JSON, from `ime.NeedsTranscriptionForLanguage` (as
   `server.go:841` does for texts). `vocabulary-app.js` and `models-app.js`
   hide the column header, cells and form field when false.
3. `internal/ai/review_text.go`: when `size > maxTextReviewChars`, split body
   and the reviewed part with a pure `splitParagraphs`, group paragraphs into
   chunks that fit, run the existing call per chunk, and merge: chunk
   suggestion where the chunk was not ok, the original chunk otherwise, joined
   with the original separators; `ok` only if every chunk is ok; reasons
   joined. One call under the limit is unchanged. Vocabulary list is passed
   whole in each call only if it fits, otherwise omitted for chunks (to be
   confirmed during implementation by measuring the prompt).
4. `internal/ai/chunk.go`: for kinds `transcription` and `translation`
   only, `GenerateChunked` checks each chunk reply's paragraph count against
   the chunk's own (`splitParagraphs`); on mismatch it regenerates that chunk
   paragraph by paragraph (reply blank lines collapsed to a line break) and
   joins with the original separators. The checkpoint stores the verified
   chunk output. Chunks that already match cost no extra call.
5. Tests: `splitParagraphs` and merge (pure), `ReviewResource` over the limit
   with a fake LLM (aligned, mismatched, oversized paragraph), generate sync
   without a section, detail JSON field.
6. Live check on the dev cluster: UI screenshot of a no-transcription list at
   1280 and 1024 px; one review of a long dev text with the fake/no-LLM path
   (at most 1-3 real Ollama calls, only if the user agrees).
7. Prod cleanup SQL drafted, run read-only first for counts, executed only
   after approval.

## Affected Areas

- `phraseforge/internal/generate/generate.go` (+ tests)
- `phraseforge/internal/ai/review_text.go` (+ tests)
- `phraseforge/internal/server/vocabulary.go`, `models.go` (+ tests)
- `phraseforge/internal/server/static/js/vocabulary-app.js`, `models-app.js`
- Prod DB (cleanup of existing rows), only on approval

## Out of Scope

- Making text 95 reviewable: its 69/68 paragraph mismatch needs a data fix or
  a different split rule (user chose fail on mismatch).
- Repairing the 27 existing prod texts whose parts already differ in
  paragraph count (a data fix, not code); dialog turns inside one paragraph
  are not counted.
- Changing prompts, the 12000 limit, or the review dialog.
- Hiding the transcription of texts/dialogs (already follows `needs_transcription`).
- Commits: the user commits manually.

## Implementation Notes

Steps, each shown to the user before the next:

1. `generate.go`: `dropUnneededTranscription` clears the model's transcriptions
   after parsing, in `HandleGenerateVocab` and `HandleGenerateModels`, when
   `EffectiveSections` has no transcription snippet.
2. `server/vocabulary.go`, `models.go`, `server.go`: `needsTranscription` in the
   list detail JSON (`languageHasTranscription`, the same rule). The list
   tables in `vocabulary-app.js` and `models-app.js` omit the column.
3. `ai/review_text.go`: over-limit reviews go through `reviewInChunks`
   (paragraph runs, `planTextChunks`, `mergeChunkFindings`); `reviewOnce` is the
   unchanged one-call path. The stored response of a chunked review is an array
   of the chunk replies.
4. `ai/chunk.go`: `alignParagraphs` after each transcription/translation chunk.
5. Prod cleanup run on request (see Validation).

Tests added: `review_text_chunks_test.go`, `chunk_paragraphs_test.go`,
`generate/transcription_test.go`, `server/vocabulary_transcription_test.go`.
Fake model only; no Ollama call.

Open decision: the column follows "marked as needing one OR has a snippet"
(what generation does). The item form field follows the flag only
(`editor.js`), so a language with a snippet but no flag shows the column and
hides the form field.

## Validation

- `go build`, `go vet`, `gofmt` clean; `go test ./...` passes on `main` with the
  dev DB (`PHRASEFORGE_TEST_DSN`), baseline was green too.
- Dev cluster deployed with `task deploy-phraseforge`. Browser check (Playwright,
  1280 and 1024 px): an `ell` vocabulary list and an `ell` models list show no
  Transcription column in the view and the manage tables; an Arabic list still
  shows it. With a temporary transcription snippet on `ell` the column appears
  but the form field stays hidden (the open decision above). Dev data restored.
- Prod (jdpct101), on the user's approval: one transaction merged 3 colliding
  phrases (`DELETE 3`, 4 examples copied) and cleared 25 transcriptions
  (`ell` 24, `deu` 4 of which 3 merged); `models_phrases` 0. Guards expected 3
  and 28 and 0 left. Read-only check afterwards: list 54 has no transcription,
  no dangling links, `arb` untouched. The backup step failed silently, so there
  is no restore point.
- Text 95 still fails to review: 69 body vs 68 transcription paragraphs, now
  with a message naming the counts.

## Documentation Review

- `phraseforge/CHANGELOG.md` needs three `Fixed` entries (user-facing); the root
  and other artifacts' changelogs need none (nothing under `shared/` changed).
- `phraseforge/README.md` described the 12000-character failure; updated.
- Constitution files unaffected.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: three entries under `## [Unreleased]` / `### Fixed`.
- `phraseforge/README.md`: the review paragraph now describes the chunking.

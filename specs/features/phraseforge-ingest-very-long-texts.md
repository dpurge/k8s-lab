---
title: Ingest very long texts reliably (context-safe chunks, resumable, visible progress)
kind: bugfix
status: implementing
version: 2
updated: 2026-10-05
branch: main
---

## Problem / Motivation

The user needs ingestion to give usable results for very long texts. Today it
cannot be relied on for them. Found while fixing empty generated vocabulary
(`phraseforge-fix-empty-generated-vocabulary`, whose Out of Scope names this gap):

1. **Chunks can overflow the context window.** Every ingest LLM step goes
   through `ai.Service.GenerateChunked`: process_text/process_dialog (cleaning),
   title (first chunk only), and the follow-up `llm_generate` jobs
   (one translation per locale, plus transcription). `chunkRunes` assumes 3
   runes per token and gives half the window to input. For diacritized Arabic
   gemma4:12b measured about 1.6 runes per token (3618 runes -> 2238 tokens), so
   a full chunk (10752 runes at `numCtx` 8192; 4608 runes at the assumed default
   4096 for translation/transcription, whose `numCtx` is 0) is roughly 6.5k
   tokens of input plus a reply of similar size: over 8192. Ollama truncates
   silently, so the result is a cleaned/translated text with missing parts and
   no error. The estimate comes from one sample and one model.
2. **One failed chunk loses all finished work.** `generateChunks` returns an
   error as soon as one chunk fails ("chunk N of M"), discarding the chunks that
   succeeded. On the CPU-only prod node a single call takes minutes (prod logs
   2026-10-05: transcription calls of 509s and 537s; generate_vocabulary 794s),
   so a 100k-character text means dozens of calls and hours per step, and a
   failure near the end repeats everything on Retry.
3. **No visibility.** A long job shows only running/done; the user cannot tell
   whether it is progressing or stuck.
4. **Chunk seams.** Chunks are joined with a blank line, and a paragraph or line
   longer than the limit is cut by runes, mid-sentence, which can leave visible
   breaks in cleaned or translated output.

Decisions (user, 2026-10-05, "go ahead with your defaults"): the limit above is the
scope of "very long"; chunk sizing is worst-case (1.5 runes/token) for all
kinds; finished chunks are persisted so a Retry resumes. Assumptions: "very long" means up to the configured ingest limit
(`ingest.maxContentBytes`: default 24 KiB, 200 KiB in the prod ConfigMap,
roughly 100k characters of Arabic); the prod node is CPU-only, so hours are
acceptable if the job is resumable and visibly progressing; the output must be
correct and complete rather than fast.

## Acceptance Criteria

- [ ] No ingest LLM call (cleaning, title, translation, transcription) sends input whose tokens plus expected reply exceed the call's `numCtx` for dense scripts, including diacritized Arabic, at the 1.5 runes/token assumption used by item generation; translation/transcription chunk sizing no longer relies on an assumed default window when `numCtx` is 0.
- [ ] A reply cut off by the context window is detected and treated as an error or a split-and-retry of that chunk, never accepted as a complete result. (Mechanism chosen in the spike, see Approach.)
- [ ] Completed chunk results are persisted as the job progresses; a Retry of a failed job resumes at the first incomplete chunk and does not repeat finished chunks.
- [ ] A long job exposes progress (chunk N of M) in the Jobs menu while it runs.
- [ ] Chunk boundaries fall on paragraph, then line, then sentence ends before any mid-sentence rune cut; cleaned and translated output is joined without breaking the text's paragraph structure.
- [ ] Tests (no network, injected LLM): sizing for dense and Latin text, truncation handling, resume after a failed chunk, boundary choice, progress reporting.
- [ ] In dev, ingesting a text of at least 30k characters of diacritized Arabic produces complete cleaned text, a title, and all translation/transcription follow-ups with no truncation; `cd phraseforge && go test ./...` passes.

## Approach

Ordered, each step shippable and reviewed before the next. Proposed, pending user decisions below:

1. **Spike (done, dev, `gemma4:12b`, hard caps of 90s):** Ollama never errors
   when the window is exceeded. Input larger than `num_ctx` is cut silently
   (2238-token prompt at `num_ctx` 1024: `prompt_eval_count` 1027, no error). A reply
   that outgrows the window makes Ollama shift the context, dropping the
   oldest tokens including the system prompt, and still ends with
   `done_reason: "stop"` (prompt 616 + reply 958 tokens at `num_ctx` 1024).
   `done_reason` is therefore useless as a signal; `prompt_eval_count +
   eval_count >= num_ctx` fires in both cases and stays well below `num_ctx`
   in a healthy call (2217 + 1653 against 8192). `shared/llm` does not read
   these fields today, and OpenRouter responses carry usage differently, so the
   guard applies to Ollama only. Decision: **detect and split** (halve the chunk
   at the best boundary and redo both halves, a bounded number of levels, then
   fail with a clear error) rather than detect-and-fail, because scripts denser
   than the 1.5 runes/token assumption (CJK) can still overflow, and failing
   would just repeat on Retry.
2. **Safe sizing:** make `GenerateChunked` size chunks by a conservative
   runes-per-token for every kind (default 1.5, as for item generation) and
   resolve the real `numCtx` instead of the 4096 guess; keep `chunkRunes` tests
   updated deliberately. Trade-off: about twice as many calls for Latin text.
3. **Truncation guard:** `shared/llm`'s `Response` gains `PromptTokens`/`ReplyTokens` from Ollama's final stream chunk (additive; `Complete` unchanged); `callLLM` turns `prompt + reply >= numCtx` into a `ContextOverflowError`; `GenerateChunked` (and item generation, via `GenerateLines`) halves an overflowing chunk, preferring a paragraph break, and reassembles the replies.
4. **Resumable chunks:** persist each finished chunk's output on the job (the
   `jobs.step`/result mechanism already carries ingest's "row created" marker;
   a per-chunk checkpoint extends it), skip finished chunks on Retry.
5. **Progress:** report "chunk N of M" through the same job record and show it in
   the Jobs menu.
6. **Better seams:** sentence-boundary fallback before a rune cut.
7. **Verify in dev** with a long diacritized Arabic text through the real queue.

Risks: more LLM calls (slower for long Latin texts); checkpoint size in the
job row for very long outputs; changing shared `GenerateChunked` also affects
the manual Translate/Transcribe buttons (same function), which is intended.

## Affected Areas

- `phraseforge/internal/ai/chunk.go`, `ai.go` (`HandleGenerate`), tests
- `phraseforge/internal/ingest/ingest.go` and tests
- `phraseforge/internal/jobs/jobs.go` (progress/checkpoint), `internal/server/jobs.go` and the Jobs menu UI
- `shared/llm/llm.go` (token counts from the Ollama response; shared with knowledge, so both changelogs get an entry if user-facing)
- `phraseforge/CHANGELOG.md`

## Out of Scope

- Changing prompts, models, or the single-worker job queue.
- Changing the existing `ingest.maxContentBytes` limit or adding other length limits (not requested).
- Item generation (vocabulary/models), already handled by `phraseforge-fix-empty-generated-vocabulary`.
- Speeding up generation (GPU/other model); only correctness and resumability.

## Implementation Notes

Working on `main`, nothing committed (the user commits). Baseline before changes: `go test ./...` green.

- **Step 1 (done): spike.** Result recorded in Approach step 1; decision: detect and split.
- **Step 2 (done): safe sizing.** `internal/ai/chunk.go`: one `chunkRunes(numCtx)` for every kind at `runesPerToken = 1.5` (8192 -> 5376 runes, 4096 -> 2304); the 3 runes/token sizing and the separate dense-ratio function are gone, so item generation and ingest/translate/transcribe share it. `effectiveNumCtx` turns an unset `NumCtx` into 4096, and `Service.generateDefault` (used by `Generate`) sends that window explicitly with the call, so the chunk size and the call agree. Evidence for 4096: Ollama reports `context_length` 4096 in dev (0.35.0) and prod runs 0.34.1 CPU-only with no context override. Item purposes (`vocabulary_item`, `models_item`; `NumCtx` deliberately 0 to match prompt-eval) do not go through `Generate` and are unchanged. Tests: `chunk_test.go` (sizes, fit for diacritized Arabic with an equal reply, explicit `NumCtx`).
- **Step 3 (done): overflow guard with split and reassembly.** Preference from the user: when in doubt send smaller chunks, split between paragraphs, reassemble correctly.
  - `shared/llm/llm.go`: `Response.PromptTokens`/`ReplyTokens` parsed from the final stream chunk (`prompt_eval_count`, `eval_count`); zero when not reported. Additive, `Complete`/`Chat` signatures unchanged; knowledge builds and its tests pass.
  - `internal/ai/overflow.go`: `ContextOverflowError`; `contextOverflow` (nil when `numCtx` unknown or no counts reported, so item calls with `NumCtx` 0 and OpenRouter are unaffected); `generateWithSplit` halves an overflowing chunk up to `maxSplitDepth` (4), then fails with the overflow error; the reply of an overflowed call is discarded; a fitting chunk makes one call and its reply is untouched.
  - `internal/ai/split.go`: `splitHalf` takes the boundary nearest the middle at the coarsest tier that leaves both halves at least 25% of the runes: paragraph break, line break (never half of a blank line), sentence end (Latin only before whitespace, so `3.14` is safe; CJK/Arabic/Devanagari full stops), word, rune (only text without whitespace).
  - Reassembly: halves are rejoined with the separator they were split on (blank line for paragraphs, newline for lines, space for sentences/words, nothing for CJK or rune cuts); `GenerateLines` always joins with a newline, since item replies are parsed line by line.
  - `callLLM` now uses `Chat` to read the counts and logs `prompt_tokens`, `reply_tokens`, `num_ctx` (counts only) and outcome `context_overflow`.
  - `GenerateChunked` and the vocabulary/models handlers (`GenerateLines`) use it.
  - Tests: `split_test.go`, `overflow_test.go` (includes a round-trip property test: an echo model that overflows above a limit returns the original text for five limits, and a `callLLM` test against a fake Ollama reporting counts); `llm_test.go` (token counts parsed or zero).
  - Found while testing: the last-resort rune cut could land next to a space and reassembly would then drop it (`a single` -> `asingle`); fixed by adding the word tier before the rune cut.
  - Not yet seen against the real model (step 7).
- **Step 4 (done): resumable chunks.**
  - `internal/checkpoint` (new): `Store` (per-job, key -> list of chunk results), `With`/`Start`/`Run.Done`/`Run.Record`. The key is the call name plus a hash of every chunk's text, so a changed text, chunk size or purpose never reuses a result; a nil `Run` (no store in the context) does nothing; load/save failures are logged and cost only a repeated chunk; `Record` keeps results as an ordered prefix and saves even if the job was just cancelled.
  - `internal/db/schema.sql`: `jobs.checkpoint jsonb` (idempotent `ADD COLUMN IF NOT EXISTS`, applied by the migrate job). `internal/jobs/jobs.go`: `jobCheckpoint` store (`jsonb_set` save, `checkpoint -> key` load); `runOne` puts it in the handler's context, so handlers need no new parameter; `Retry` copies the checkpoint inside the same `INSERT` (the worker cannot claim the new job before it has it); `complete` clears it when the job is `done` and keeps it when failed or cancelled; server restarts (`FailStale`) therefore resume too.
  - `ai.generateChunks` records and resumes each chunk (a single chunk too, so a failure in a later step such as the title does not redo the cleaning); `generateItems` saves each chunk's whole outcome (items, skipped count, unfixed lines), so a resume repeats neither generation nor correction rounds, and dedupe/skipped counts are unchanged.
  - Tests: `checkpoint_test.go` (reuse, no cross-call reuse, ordered prefix, store failures, save after cancel); `chunked_generate_test.go` (fails on chunk 3 of 4, retry calls only the unfinished chunks and returns the complete result; single chunk; unreadable saved reply regenerated); `correct_test.go` (items resume without repeating generation or correction); `jobs/checkpoint_db_test.go` (real Postgres, skipped unless `PHRASEFORGE_TEST_DB` is set: save/load, `Retry` copies, clear on done only). Run against the dev database via port-forward: all pass, no rows left.
  - Deployed to dev (`task deploy-phraseforge`); migration confirmed (`checkpoint jsonb`).
- **Step 5 (done): progress in the Jobs menu.**
  - `schema.sql`: `jobs.progress text` ("3/12", finished/all chunks); `jobs.Job.Progress` (in `jobColumns`/`scanJob`, so `claim`'s RETURNING stays in sync); `jobCheckpoint.SetProgress` also moves `updated_at` (a liveness signal); `complete` clears it when `done` and keeps it when failed or cancelled, to show where the job stopped.
  - `checkpoint`: optional `ProgressReporter` on the Store; `Run` reports done/total at `Start` (so a resumed run shows where it picks up) and after each recorded chunk; nothing for a single chunk; a failing report is logged, never fatal. Covers both `generateChunks` and `generateItems`, since both use `Run`.
  - `internal/server/jobs.go`: `jobSummary` (extracted, tested) adds `progress` to the list rows, omitted when empty; the detail view gets it from `jobs.Job` JSON. `i18n`: `jobs.view_progress` in en and pl, added to `jobsAppI18nKeys`.
  - `static/js/jobs-app.js`: the Status cell shows `running (3/12)`; the detail dialog gets a Progress row when present (`node --check` passes; there is no JS test harness for this page, so the rendering itself was not exercised in a browser).
  - Tests: `checkpoint_test.go` (start/after-chunk reports, single chunk, plain and failing stores), `server/jobs_test.go`, `jobs/checkpoint_db_test.go` (set, read back via `Get`, cleared only on done). The DB tests ran against the dev Postgres and pass; migration confirmed (`progress text`).
  - Mistake made and caught while verifying: a relative `KUBECONFIG` stopped resolving after a `cd`, so three read-only `kubectl` calls went to the default context (a GKE cluster) and failed with NotFound; nothing was read or changed there. Reran with an absolute path after checking the context is `k3d-k8s-lab`.
- **Step 6 (done): better seams.**
  - `internal/ai/chunk.go`: the splitter is now tier-driven (`chunkTiers`: paragraph, line, sentence, word; rune cut only for text with none of those, such as unpunctuated CJK). Paragraph and line behaviour is unchanged (the existing tests pass untouched). `splitSentences` reuses `sentenceEnds` (a Latin terminator counts only before whitespace, so `3.14` and `e.g.x` stay whole; CJK, Arabic and Devanagari full stops need none).
  - Each piece records the separator it was cut at (`chunkPiece.sepBefore`: blank line, line break, space or nothing); `GenerateChunked` passes them to `generateChunks`, which joins the replies with them (nil falls back to the old blank-line/newline default). Before, every chunk boundary was joined with a blank line, so a paragraph cut mid-way came back as two paragraphs.
  - Tests: a round-trip property (a text with paragraphs, lines, Latin/CJK/Arabic sentences and an unbroken run, cut at nine sizes from 8 to 300 runes, rejoins to exactly the original, every piece within the limit); sentence-end, word and rune-cut cases; `splitSentences` table; separator-aware join.
  - Note: `splitHalf` (overflow splitting, step 3) and `splitSentences` both encode "what ends a sentence" via the shared `sentenceEnds` constant but are separate loops; left as is.
- Step 7: not started.

## Validation

Not started.

## Documentation Review

Not started.

## Documentation Updates

Not started.

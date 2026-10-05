---
title: Better transcription, grammar, translation, notes and models prompts (eval first, then app and Helm)
kind: feature
status: implementing
version: 5
updated: 2026-10-05
branch: main
---

## Problem / Motivation

The user is not satisfied with what the LLM prompts produce for language
lessons and wants them improved, tested first in `prompt-eval` (promptfoo,
local Ollama `gemma4:12b`), then ported to the app (`phraseforge`) and the
deployed Helm values (`jdp-helm/jdp-frontend`). Wishes, as stated:

1. **Transcription**: close to speech; the scientific transliteration used in
   language handbooks; capitalized as in normal text; precise; never drops
   accented/diacritic characters.
2. **Vocabulary**: the dictionary form of a word is preferred.
3. **Translations**: like a dictionary's: not capitalized unless a name that is
   normally capitalized; several senses separated by `; `, never `/`.
4. **Notes**: too many today and written in English even when the translation
   is Polish. Notes must be in the translation's language and only when the
   word could be misunderstood without them; skipping the note is the default.
5. **Models**: build phrases from simple to more complicated; include only key
   phrases with a complicated structure.
6. **2-shot examples** in the prompts to show what is expected.

Inferred from the user's phraseforge skills (`phraseforge-format`,
`phraseforge-core`, `phraseforge-lang-arb`): JSON entry fields `phrase`,
`grammar`, `transcription`, `translation`, `notes`; POS-first space-separated
grammar tags (`N m sg`, `V`, `Adj`); Arabic transcribed in DIN 31635 as a
"natural reading" hybrid (sun-letter assimilation, hamzat al-waṣl elision,
case endings dropped, sentence-initial and proper-noun capitals, Latin
punctuation only, all diacritics kept); translations in Polish, senses joined
with `; `, full Polish diacritics, no parentheses inside `translation`;
`notes` almost always null; verbs in the 3rd person masculine singular
perfect, nouns in the singular with gender, adjectives masculine singular
indefinite; models are 3-6 progressive phrases.

Decisions (user, 2026-10-05, "Approved"): all six defaults accepted, and
amended: the test languages are **arb, cmn, fas, deu, heb, lat, ron, spa, tur**
(not only Arabic); examples for the tests are excerpts of the user's own
chapters in `epub-public/src/txt/lang-notes` (their curated vocabulary, models,
transcription and translation are the gold references; where the gold chapter
predates a newer rule, such as dropping Arabic case endings, the user's current
language skill wins); the tests must be **short**, so the user can re-run them
quickly; the per-language sections the user will copy into the database come
from the eval's section files.

Further decision (user, 2026-10-05): Hebrew texts in the user's chapters are
pointed (niqqud), which is why their chapters carry no transcription, but real
texts are unpointed (and Arabic/Persian are mostly unvocalized), so the
transcription is **necessary** for every non-Latin language: the tests must
require it, including for unpointed/unvocalized input, and the application must
generate it. The app already does when `ime_config.needs_transcription` is on for
the language and its transcription section is set (both are database settings,
not seeded: `needs_transcription` defaults to false), so no code change is
planned; the user switches them on per language. The transcription sections tell
the model to supply the standard dictionary reading when the text has no vowel
signs, never to skip the transcription.

Scope additions (user, 2026-10-05, during step 1): the eval also covers **fra,
ind** (synthesized, few tests; the `lang-notes` folders for them hold no
chapters), then **vie, kor, jpn, swe, ukr** (synthesized; only Japanese needs a
transcription), then **swa, fin, dan, nld, ita, grc, ell, yid, tgl** (synthesized, few
tests; `lat` was already in the main list). 25 languages in all; Yiddish needs
a transcription (YIVO), Tagalog has no skill (general conventions, like
Indonesian). Script codes
are ISO 15924: Japanese is `jpan` (the user's `phraseforge-lang-jpn` skill says
`japn`, which the user confirmed is an error in the skill). The user also asked
that **all these languages be registered in the application, plus English and
Swahili**: the app's `language` seed lacked `eng`, `fin`, `jpn`, `kor`, `swa`,
`swe` and `tgl` (Polish, Yiddish and the rest were already there), so they are added to
`internal/db/schema.sql` (applied by the migrate job, idempotent). Swedish and
Swahili have no per-language skill; their sections follow the closest analog
(Danish) and general conventions, which is an assumption for the user to review.

Scope addition (user, 2026-10-05, during step 3): also improve the **import
cleaning prompts** (`processText`, `processDialog`): the user sees navigation
left behind, the text's introduction preserved and questions after the text
preserved, and wants only the clean text itself kept (the article headline stays
part of the text). The eval gets cases for exactly these (a lesson-page
introduction before the text, questions and "read also" links after it, in arb,
deu, spa, cmn and a dialog), measured on the current prompts first. Also,
the user said the tests need not be perfect: a good starting point, to be
adjusted by them later, and time matters.

Facts found while preparing this spec:

- Prompts live in three copies kept in step by `prompt-eval/scripts/sync-prompts.js`
  (`npm run check-sync`, currently passing): the Go defaults in
  `phraseforge/internal/config/config.go` (`DefaultVocabularyItemPrompt`,
  `DefaultModelsItemPrompt` and the purposes' defaults), the eval files
  (`generate/prompts/*.txt`, `vocabulary-translation/prompts/system.txt`,
  `models-item/prompts/system.txt`) and the Helm values that prod actually runs,
  which are already tuned beyond the Go defaults for the `generate*` purposes.
- The affected purposes are `processText`, `processDialog`, `transcription`, `generateVocabulary`,
  `generateModels`, `vocabularyItem`, `modelsItem`. The full-text `translation`
  prompt is prose translation (see question 1).
- The per-language `{{grammarPrompt}}` and `{{transcriptionPrompt}}` sections
  are stored in the database (Admin > LLM > Language sections), not in git or
  Helm, so they cannot be "ported" by this work; only the prompt templates can.
- The eval has no Arabic data (only cmn, deu, spa, all to Polish) and its
  assertions check structure and a few references, not the style rules above, so
  none of the wishes can be measured today.
- The dev Ollama is shared with the dev app, whose job queue is single-worker, so
  an eval run competes with a busy queue.

## Acceptance Criteria

- [ ] `prompt-eval` covers the nine languages (arb, cmn, fas, deu, heb, lat, ron, spa, tur, all to Polish) for the five purposes with short cases taken from the `lang-notes` chapters: a few reference items per language (expected dictionary form, grammar tag, transcription, dictionary-style Polish translation, note policy) and one short excerpt per language for transcription, vocabulary and models generation.
- [ ] For every language that needs one (arb, cmn, fas, heb, jpn, yid; not kor, grc, ell, ukr) the tests require a non-empty transcription in Latin script, with diacritics and capitalization checked, on pointed and unpointed (Hebrew) or vocalized and unvocalized (Arabic, Persian) input.
- [ ] Each of those languages has its grammar section file (and its transcription section file where the script is non-Latin) in `prompt-eval`, in the shape of the existing `cmn-*.txt`, ready for the user to copy into Admin > LLM > Language sections.
- [ ] A full run is short enough to repeat (the measured duration is recorded in Validation), and `SMOKE=1` plus a `LANGS=arb,cmn` filter run a subset in a minute or so.
- [ ] New deterministic checks measure the wishes: translation not capitalized unless listed as a name, senses joined by `; ` and no `/` or parentheses; `notes` null unless the item is marked as needing one, and when present in Polish (not English) for a Polish target; transcription keeps diacritics, has sentence-initial capitals and no case endings for Arabic, with a similarity floor against the reference; vocabulary phrases are in dictionary form; models lines get longer line by line, are few, and are not simple one-clause sentences.
- [ ] A baseline run of the current prompts is recorded (per-check pass rates) before any change, so improvement is measured, not asserted.
- [ ] The five prompts are rewritten, each with 2 worked examples (2-shot), until the new checks pass at a recorded, higher rate than the baseline on Arabic without regressing the existing cmn/deu/spa suites (`npm test` and the suites keep passing).
- [ ] The Go defaults and the dev ConfigMap in `phraseforge` match the new prompts, `npm run check-sync` passes against them and the edited Helm values, and `cd phraseforge && go test ./...` passes.
- [ ] `jdp-helm/jdp-frontend/values.yaml` carries the same prompts for the five purposes (and its chart changelog is updated if the repo keeps one), uncommitted, on `main`.
- [ ] For the per-language sections, ready-to-paste texts for Arabic (and the existing Mandarin ones kept) are delivered as files in `prompt-eval`; applying them in the admin UI is left to the user.

## Approach

Ordered, each step shippable and reviewed before the next (small steps):

1. **Language registry and sections** (`prompt-eval/lib/tests.js` `SOURCES`,
   `vocabulary-translation/prompts/<lang>-grammar.txt` and `-transcription.txt`):
   the nine languages and their section files, from the user's per-language
   skills and the tag sets seen in the `lang-notes` chapters.
1b. **Eval data** from the chapters (`vocabulary-translation/data/<lang>-pol.yaml`,
   `datasets/generate-*.yaml`, `datasets/model-patterns.yaml`): short excerpts and a
   few reference items per language; a `LANGS` filter next to `SMOKE`.
2. **Style checks** in `prompt-eval/lib` (with `node --test` unit tests):
   deterministic, as the existing checks are; no LLM judge.
3. **Baseline**: run the affected suites with the current prompts on dev Ollama,
   record per-check pass rates in the spec.
4. **Rewrite and iterate** prompt by prompt (transcription, generateVocabulary,
   generateModels, vocabularyItem, modelsItem), each with 2-shot examples chosen
   to match the format the app parses (`phrase {grammar} [transcription] =
   translation` lines for the generate purposes, JSON for the item purposes); one
   prompt per iteration, re-run its suite, keep only changes that improve the
   measured checks without regressions.
5. **Port to the app**: update the Go defaults and `phraseforge/k8s/configmap.yaml`
   (dev), run `sync-prompts` / `--check`, update CHANGELOG, run the Go tests.
6. **Update Helm** `values.yaml` for the five purposes; run `--check` against it.
7. **Verify in dev**: deploy, regenerate one Arabic text's vocabulary, models and
   transcription and compare with the baseline.

Design choices: language-specific rules (transcription system, grammar tag set)
stay in the per-language sections, so the generic prompts say "use the standard
scholarly transliteration for the source language" and explain the style, not a
fixed system; the 2-shot examples use Arabic -> Polish (the user's main pair).
Alternatives: an LLM judge for style (rejected: slow, non-deterministic, and the
existing suites are deterministic); editing the live Helm values first (rejected:
the user asked for eval first).

Risks: `gemma4:12b` may not follow every rule at once, so a rule may need to be
split between prompt and per-language section; longer prompts (examples) cost
prompt tokens on every item call (about 145 item calls per text), which on the
CPU-only prod node costs time; the prompt change alters `check-sync` expectations
in two repos at once.

## Affected Areas

- `prompt-eval/` (datasets, data, `lib/` checks and tests, `generate/prompts/*.txt`, `vocabulary-translation/prompts`, `models-item/prompts`)
- `phraseforge/internal/config/config.go`, `phraseforge/k8s/configmap.yaml`, `phraseforge/CHANGELOG.md`, tests that pin prompt text if any
- `jdp-helm/jdp-frontend/values.yaml` (and its CHANGELOG if present)
- `specs/` (this spec, roadmap)

## Out of Scope

- The full-text `translation` prompt (the user chose to keep it natural prose), `title`, `processText`, `processDialog`.
- Editing the database-stored language sections in any cluster, or seeding them from code.
- Changing the model, parameters (`numCtx`, timeouts) or the response schema.
- Committing or pushing anything in any of the three repositories; releasing a new phraseforge image or deploying to `jdpct101`.

## Implementation Notes

Working on `main` in all three repos, nothing committed (the user commits).

- **Baseline taken first (at a clean `prompt-eval` HEAD, before any edit)**, `SMOKE=1`, `--no-cache`, `gemma4:12b` on the dev Ollama (idle at the time), concurrency 4:

  | Suite | Result | Duration |
  |---|---|---|
  | vocabulary-translation | 20/23 pass (1 translation, 2 grammar failures) | 1m54s |
  | models-item | 20/20 | 1m10s |
  | generate-transcription | 2/2 | 11s |
  | generate-generateVocabulary | 2/2 | 1m27s |
  | generate-generateModels | 2/2 | 43s |

  Reading: the current checks are lenient (case, diacritics and punctuation are normalized away, notes and `/` are not checked), so a passing baseline says little about the user's complaints. Step 2 adds checks that can see them.
- **Step 1 (done): language registry and section files** in `prompt-eval`. `lib/tests.js` `SOURCES` now has arb (`Standard Arabic`), cmn, deu, fas (`Persian`), heb, lat, ron, spa, tur, with the names phraseforge sends (the `name` column seeded in `internal/db/schema.sql`); a `LANGS=arb,cmn` filter works next to `SMOKE=1` in `referenceTests`, `structureTests` and `generateTests`. `vocabulary-translation/prompts/<lang>-grammar.txt` for all nine languages (POS tags from the core skill's canonical set, gender/number/class modifiers per the language skills and the tag sets seen in the `lang-notes` chapters) and `<lang>-transcription.txt` for arb (DIN 31635, natural reading), cmn (Pinyin), fas (DIN 31635 adapted for Persian), heb (SBL, Modern Hebrew). The existing cmn, deu and spa grammar files and cmn transcription were rewritten to the same shape (they were 24-237 bytes). Sections are 0.5-1.2 KB each, since every item call pays their tokens. `npm test`: 35/35.
- Finding: the deployed `generateVocabulary` prompt lists the tags `J` (adjective) and `A` (adverb), but the user's canonical set (core skill, chapters) is `Adj` and `Adv`; the rewrite in step 4 should use the canonical tags.
- **Step 1 (continued): scope additions.** Sections and registry entries now cover 23 languages (arb cmn dan deu ell fas fin fra grc heb ind ita jpn kor lat nld ron spa swa swe tur ukr vie); Hepburn transcription section for jpn; fas long vowels corrected to `ā ī ū` (the user's skill examples and chapters), and the Latin section drops the optional declension number (the chapters tag `N f`). Sections for the older arb, fas and heb now tell the model to supply the dictionary reading for unvocalized/unpointed text.
- **Step 1b-i (done): item reference data** `vocabulary-translation/data/<lang>-pol.yaml` for all 23 languages, 93 reference rows (3-5 per language: main languages from the chapters' own vocabulary lines, synthesized ones from the skills' examples; arb and heb include an unvocalized/unpointed row; transcriptions required for arb, cmn, fas, heb, jpn). Caveats for the user to review: the transcription references for Hebrew (the chapters carry none) and the Arabic/Persian ones were written by me from the skills' rules, and the Persian skill rule (ص is `s`) differs from the chapters (`ṣāderāt`), so those letters are avoided in the items. References hold one primary sense; the new checks (step 2) will accept more senses after it.
- **App registration (done):** `eng`, `fin`, `jpn`, `kor`, `swa`, `swe` added to the `language` seed; verified in the dev database after `task deploy-phraseforge`: all 25 requested codes present (`arb cmn dan deu ell eng fas fin fra grc heb ind ita jpn kor lat nld pol ron spa swa swe tur ukr vie`); CHANGELOG entry; `go test ./...` passes.
- **Yiddish and Tagalog added:** `yid` (grammar with `N m/f/n` and the article in the phrase, YIVO transcription section from the user's skill, 4 items with transcription) and `tgl` (grammar section, 4 items, no skill). `tgl` is added to the app seed. The eval now has 25 languages and 101 reference rows; with `eng` and `pol` (app list only) that is 27 codes, all verified present in the dev database after `task deploy-phraseforge`.
- **Standard text representation (user: "use standard way of representing texts"):** the Yiddish item now uses the standard spelling `קליין` (two plain yuds) instead of the ligature `קלײן` from the user's skill example. A new unit test `test/representation.test.js` keeps the eval's section files, item data, models-item data and generate datasets in Unicode NFC, free of Arabic/Hebrew presentation forms, with Yiddish digraph ligatures only in the pasekh form (`ײַ`), and Romanian with comma-below ș ț (cedilla ş is correct for Turkish only); it has a second test proving each check fires. `phrase-robustness.yaml` is excluded on purpose (it holds unnormalized text to test the prompts). A Hebrew reference was corrected while writing the data (`חדר` is `ḥeder`).
- **Step 1b-ii (done): generate datasets.** One short excerpt per language appended to `datasets/generate-generateVocabulary.yaml` and `generate-generateModels.yaml` (28 tests each: the 25 languages, the Hebrew unpointed variant, and the 3 existing rows) and, for the scripts that need it, `generate-transcription.yaml` (9 tests: arb, fas, heb pointed and unpointed, jpn, yid, plus the 3 existing cmn rows). Gold excerpts and references come from the chapters (arb: the unvocalized body text with the chapter's transcription; fas: two sentences, one reference letter changed from the chapter to follow the skill, `manṭaqe` -> `manteqe`; deu, spa, tur, lat, ron, cmn) and are synthesized for the rest. Hebrew, Japanese and Yiddish references were written by me from the skills' rules, for the user to review. `SMOKE=1` keeps 4 tests per suite (arb, jpn and the existing smoke rows).
- **Step 1b-iii (done, not wired in): models-item references** `models-item/data/<lang>-pol.yaml`, 26 rows (one key phrase per language, two for Hebrew; gold from the chapters for arb, fas, cmn, deu, spa, tur, lat, ron; transcriptions for arb, fas, cmn, heb, jpn, yid). They are not used by the models-item suite yet: its checks arrive in step 2, and the baseline stays reproducible until then.
- **Step 2 (done, deliberately rough per the user): style checks.** `lib/style-checks.js` (translation style, notes policy, Latin-only transcription with diacritics and capitals, canonical POS tags, dictionary forms), wired into the item and generate checks, with a compact `test/style-checks.test.js`; three old tests that encoded the old leniency (tone marks ignored, accents forgiven) were updated on purpose. Also `scripts/quick.sh` (a quick run on arb, heb, jpn, deu plus the cleaning cases, about 8 minutes), `scripts/summarize.js`, `scripts/failures.js`, `SMOKE` keeps one reference row per language, and cleaning cases for an introduction before the text and questions after it.
- **Step 3/4 (done, one pass only; the user asked to stop iterating):** quick-run baseline of the old prompts, then one rewrite of seven prompts (transcription, processText, processDialog, generateVocabulary, generateModels, vocabularyItem, modelsItem), each with two worked examples. Pass counts before -> after (quick set): vocabulary item 6/10 -> 9/10 (style 8 -> 10/10); models item 11/12 -> 11/12; processText 2/4 -> 4/4 (the intro and questions cases); generateVocabulary 0/6 -> 1/6 (line format 4 -> 6/6, canonical tags 2 -> 4/6, dictionary forms 3 -> 4/6); generateModels 3/6 -> 1/6 (at most 8 phrases 3 -> 6/6, but "progressively longer" 2 -> 0/6, which the check measures as non-decreasing character length and the new prompt's examples do not satisfy; the check is a rough proxy); transcription 1/4 -> 2/4 (diacritics and case still 0/4: the generic prompt cannot carry the per-language rules, which belong in the language transcription sections the user will paste into the database); processDialog 3/3 -> 3/3. Not re-run after these results, by the user's request.
- **Step 5/6 (done): ported.** `phraseforge/internal/config/config.go` (seven prompts as `Default*Prompt` constants), `phraseforge/k8s/configmap.yaml`, `jdp-helm/jdp-frontend/values.yaml`, all generated from the eval files; `scripts/sync-prompts.js` taught to follow the constants; `npm run check-sync` passes; both YAML files parse; `go test ./...` passes; CHANGELOG entry added. Not committed anywhere.
- Step 7 (verify in dev with a real text): not done.

## Validation

Not started.

## Documentation Review

Not started.

## Documentation Updates

Not started.

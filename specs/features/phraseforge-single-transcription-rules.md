---
title: One home for transcription rules, translated dictionary label, whole-tile click
kind: bugfix
status: done
version: 2
updated: 2026-10-13
branch: main
---

## Problem / Motivation

phraseforge-prompt-snippets (done) added the `{{transcriptionPrompt}}` placeholder but left the per-script rules inline in the Admin > LLM prompts, so two rule sets coexist (user report, 2026-10-13):

- The default prompts name transcription systems (generate-transcription.txt: DIN 31635, Pinyin, Hepburn, SBL, YIVO) while Admin > Languages holds the per-language rule. User decision 2026-10-13: leave the prompt files alone; this task fixes the application (schema, API, GUI), not the prompts.
- Dev DB: 4 rows in `llm_prompts` (kind `transcription`, languages arb, cmn, fas, heb) are byte-identical copies of the `language_llm_sections.transcription_prompt` snippets (created together 2026-10-06). As whole system prompts they have no placeholder and none of the generic mechanics.
- `admin.sections_dictionary` was not in `adminAppI18nKeys` (internal/server/admin.go), so Admin > Languages showed the raw key. The i18n test only matched `T("...")`, not `label: "..."`. FIXED in this session (working tree, not committed): key added, test extended and shown to fail without the fix.
- On a tablet only the title link of a list tile (Texts, Vocabulary, Dialogs, Models) opens the item.

Decisions 2026-10-13: dev data may be changed; whole-tile click in every list; no backward compatibility for export/import; option A for Admin > LLM (settings only, prompt read-only).

## Acceptance Criteria

- [x] Admin > LLM stores only provider, model, think and timeout per (kind, source, target); `llm_prompts.prompt` is dropped, so the application cannot hold a second copy of a language's rules.
- [x] Admin > LLM shows the system prompt read-only in its own tile, apart from the Save settings button; Edit and Delete in its table are buttons and Edit scrolls to the form.
- [x] Admin > Languages lists snippets as full-width tiles with Edit and Delete buttons; Edit scrolls to the form and focuses the text; Cancel is a button.
- [x] `admin.sections_dictionary` renders translated (the i18n test also covers `label:` keys).
- [x] In Texts, Vocabulary, Dialogs and Models lists a click anywhere on a tile opens the item.
- [x] Dev data: the 4 duplicate `llm_prompts` rows are gone and all 25 prompt-eval language snippet sets are imported.
- [x] Go and JS tests pass; checked on dev with Playwright; no LLM calls.

## Approach

Option A: a row of `llm_prompts` overrides provider, model, think and timeout only; the prompt text is the purpose default (`ai.Service.prompt`), and per-language rules come from `language_llm_sections`. `ai.Prompt.Prompt` stays as the effective prompt but is `json:"-"`. The column is dropped idempotently in schema.sql. The pf-card component forwards a click on a card body to its `[data-open]` link.

## Affected Areas

- internal/db/schema.sql, internal/ai/ai.go, internal/server/admin.go, admin tests
- internal/server/static/js/admin-app.js, static/components/pf-card/*, templates/layout.html, internal/i18n/i18n.go
- dev DB tables llm_prompts, language_llm_sections

## Out of Scope

- Editing the default prompt files, prompt-eval, or k8s/configmap.yaml.
- Production DB: not checked (access to prod was not available in this session); applying the schema there drops any saved prompt text.
- Backward compatibility of the configuration export/import format.

## Implementation Notes

1. Translation key added to `adminAppI18nKeys`; the i18n test now also matches `label: "..."` and was shown to fail without the fix.
2. `pf-card` forwards body clicks to the `[data-open]` link, ignoring links, buttons, form controls and text selection; test updated; verified by touch taps on all four lists at 820 px.
3. Dev data: `pg_dump` of both tables, 25 snippet sets from prompt-eval/prompt/phraseforge/lang imported, 4 duplicate rows deleted.
4. Schema, `SetPrompt`, `ListPrompts`, `prompt()`, import and export no longer touch the prompt column; import validation trims language fields (a blank source language reached the database before).
5. Admin GUI: LLM prompt in a read-only tile, Save settings under the fields; Languages snippets as tiles; Edit/Delete/Cancel are `pf-button`s in the IME, LLM, Languages and grants tables; Edit scrolls and focuses.

## Validation

- `go vet ./...` clean; `go test ./... -count=1` passes in every package against the dev Postgres (throwaway schemas); 48 JS tests pass.
- Playwright on dev at 1280, 1024 and 820 px: tiles, Edit scroll and focus, button clicks, confirm dialogs (dismissed), no raw i18n keys, no page errors. Row counts unchanged by the clicks.
- Not verified: prod; a real tablet; the export/import round-trip test (`TestAdminConfigExportImportRoundTripDB` wipes the database, left skipped).

## Documentation Review

Constitution files: no drift. CHANGELOG.md (phraseforge): entries for the tile click, the settings-only LLM tab and dropped column, the Languages tiles and buttons, and the fixed label; two superseded Unreleased entries corrected or removed. README.md: Admin overrides and Language snippets paragraphs.

## Documentation Updates

Done in phraseforge/CHANGELOG.md and phraseforge/README.md as listed above.

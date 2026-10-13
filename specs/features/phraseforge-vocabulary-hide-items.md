---
title: Vocabulary view Actions column with Review button and per-user Hide
kind: feature
status: done
version: 1
updated: 2026-10-13
branch: main
---

## Problem / Motivation

On the Vocabulary view (phraseforge/internal/server/static/js/vocabulary-app.js, renderItemsTable) editors get a "Review" column whose per-item control is a link; the header says "Review". A learner has no way to stop seeing words they know well. User request 2026-10-13: Review becomes a button, the header becomes "Actions", and a new "Hide" action hides an item for the current user only; other users still see it.

Findings: phrases are shared across lists (phrases table, linked from vocabulary_items.phrase_id); a user's access to a list comes from roles (roles.CanView/CanEdit); vocabulary.Store.Items(listID) is also used by export, sync, review, and generation, which must keep seeing every item; the detail endpoint GET /api/v1/vocabulary/{id} serves both the View and the Manage screens.

Assumptions (defaults, change on review): hide applies to the phrase, so it is hidden in every list that contains it; hidden state is a personal preference, never exported or imported; list tile counts stay the list's total.

## Acceptance Criteria

- [ ] View table: the last column header is "Actions" (en) / "Akcje" (pl); the old "Review" column header key is removed.
- [ ] Every control in that column is a button (pf-button), including Review, Review again, and the Reviewed / N open / Outdated status that opens the review dialog.
- [ ] Every user who can view the list sees an Actions column with Hide; Review controls appear only for editors, as today.
- [ ] Clicking Hide removes that phrase from the current user's View of every list; a second user's View is unchanged (store and handler tests with two users).
- [ ] Below the table a "Show hidden (N)" toggle appears when N > 0; when on, hidden rows are listed dimmed with an Unhide button instead of Hide; Unhide restores the row.
- [ ] "Copy" markdown on the View contains only visible items.
- [ ] The Manage (edit) screen, export, review, sync and generation still see all items.
- [ ] Hide and Unhide are idempotent; a user without view access gets 404; hidden rows go away with the user, the phrase, or the list.
- [ ] Go and JS tests pass; Playwright check at 1280 and 1024 px on dev, no LLM calls.

## Approach

Data: new table `vocabulary_hidden_phrases(user_id → users ON DELETE CASCADE, phrase_id → phrases ON DELETE CASCADE, created_at, PRIMARY KEY (user_id, phrase_id))`, added idempotently in schema.sql.

Store (internal/vocabulary): `Hide(ctx, userID, listID, position)` (resolves the item's phrase_id, INSERT ... ON CONFLICT DO NOTHING), `Unhide(...)`, `HiddenPhraseIDs(ctx, userID, phraseIDs)`. `Items` is unchanged.

API (internal/server/vocabulary.go): `PUT` and `DELETE /api/v1/vocabulary/{id}/items/{position}/hidden`, allowed for anyone with CanView, scoped to the current user. GET detail adds `hidden: bool` to each item and builds `markdown` from visible items only; the client does the filtering, so the toggle needs no extra request.

UI (vocabulary-app.js): View table always has the Actions column (Review buttons only when canEdit, then Hide / Unhide); Manage ignores the hidden flag. i18n en/pl: vocabulary.hide, unhide, show_hidden, hide_hidden; drop vocabulary.review_col. Compact button size via the existing admin-table rule extended to .vocab-items-table.

Sample (View, user with 2 hidden words):

    Phrase  Grammar  Transcription  Translation  Notes   Actions
    كتاب    N        kitāb          book                 [Review] [Hide]
    ...
    [Show hidden (2)]        -> hidden rows dimmed, each with [Unhide]

Steps, each shown to you before the next: (1) schema + store + tests, (2) API + handler tests, (3) UI + i18n, (4) dev verification with screenshots, (5) docs/changelog. About 5 small steps, no LLM calls.

Risk: low. Phrase identity includes grammar and transcription, so the same word with different tags is a different phrase and is not hidden by Hide on the other.

## Affected Areas

- phraseforge/internal/db/schema.sql
- phraseforge/internal/vocabulary/ (new hide.go + tests)
- phraseforge/internal/server/vocabulary.go (+ route registration, tests)
- phraseforge/internal/server/static/js/vocabulary-app.js, templates/layout.html (CSS)
- phraseforge/internal/i18n/i18n.go, phraseforge/CHANGELOG.md

## Out of Scope

- Hiding in Models lists, Texts or Dialogs.
- Bulk hide, hide from the Manage screen, hide by tag or by list.
- Exporting or importing hidden words.
- Changing list tile item counts.

## Implementation Notes

1. schema.sql: `vocabulary_hidden_phrases`; `vocabulary/hide.go`: `Hide`, `Unhide`, `HiddenPhraseIDs` (idempotent, `ErrItemNotFound` for a missing item); `Items` unchanged.
2. `vocabulary_hide.go`: `PUT`/`DELETE /api/v1/vocabulary/{id}/items/{position}/hidden`, CanView only; `GET /api/v1/vocabulary/{id}` adds `hidden` per item and builds the Copy markdown from visible items.
3. vocabulary-app.js: the View table has an Actions column for every viewer; Review controls (editors only) and Hide/Unhide are `pf-button`s; a "Show hidden (N)" toggle shows hidden rows dimmed; i18n en/pl (`vocabulary.hide`, `unhide`, `show_hidden`, `hide_hidden`; `vocabulary.review_col` removed). Buttons wrap inside the Actions cell.

## Validation

- Store tests (3) and handler tests (2) pass against the dev Postgres; full suite and 48 JS tests pass.
- Playwright on dev at 1280 and 1024 px on a 74-item list: header "Actions", Review controls are buttons, Hide leaves 73 rows and shows "Show hidden (1)", the state survives a reload, the toggle shows 74 rows, Unhide restores; no page errors. Review was not clicked (no LLM calls); the hidden table was empty afterwards.
- Not verified: a second real user in the browser (covered by Go tests); narrow widths (the table already scrolls sideways below about 1100 px, not measured before the change).

## Documentation Review

Constitution files: no drift. memory.md: 4 entries appended. CHANGELOG.md (phraseforge): one Added and one Changed entry; the vocabulary review entry's "Review link/column" wording corrected.

## Documentation Updates

Done in phraseforge/CHANGELOG.md.

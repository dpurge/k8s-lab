---
title: Apply enlarged-script display to Transcription/Translation on Text/Dialog view
kind: bugfix
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

**Corrected diagnosis** (the original draft of this spec misdiagnosed the
root cause as a missing CSS class on two tabs — the user's own testing
caught that Source doesn't work either, on the View page specifically,
even though it *does* carry the class):

The real bug is CSS specificity, not a missing class. `layout.html:222-223`:

```css
article.prose { font-size: 1.15rem; line-height: 1.7; }
.prose-enlarged { font-size: 1.4rem; line-height: 1.9; }
```

`article.prose` (one element + one class) is *more specific* than
`.prose-enlarged` (one class alone), so wherever both classes land on the
same `<article>` — which is exactly what the View page's Source tab
already does (`texts-app.js:520`: `class="prose${... " prose-enlarged" :
""}"`) — the base rule's `1.15rem` silently wins over the enlarged rule's
`1.4rem`, even though `text.scriptEnlarged` is correctly `true` (confirmed
live: `GET /api/v1/texts/10` returns `"scriptEnlarged": true` for a Han
script text, and the class is correctly present in the rendered HTML) and
the class is correctly applied. **Source has always silently failed to
visually enlarge on the View page**, despite every other part of the chain
being correct.

Edit mode has no such bug: its enlarge rule (`layout.html:269`,
`.editor-tab-pane textarea.editor-enlarged`) is a compound selector with
*higher* specificity than its own base rule (`layout.html:268`,
`.editor-tab-pane textarea`), so it correctly wins there — confirming why
Edit mode (Source, Transcription, Translation alike) already looks right,
exactly as the user reported.

**Correction (post-delivery):** Transcription and Translation were never
supposed to be enlarged in the first place — only Source (the original
script) should respect the enlarge setting. An earlier revision of this fix
mistakenly *added* the `prose-enlarged` class to Transcription/Translation
on the View page (mirroring Source), which the user explicitly rejected:
"Only source text needs enlarging, transcription and translation should
stay normal size!" That class addition has been reverted from
`texts-app.js`/`dialogs-app.js`; only the CSS specificity fix (which is
scoped to Source, the only tab that carries the class) remains.

Vocabulary/Models table cells (`vocabulary-app.js:331`, `models-app.js:326`)
are unaffected by the specificity bug — they apply `prose-enlarged` alone,
with no competing element+class rule to lose against — so no change is
needed there.

## Acceptance Criteria

- [ ] `.prose-enlarged` actually wins visually whenever it's applied
  together with `.prose` on an `<article>` element — verified by inspecting
  the *computed* font-size in a browser, not just the class list.
- [ ] Text view: Source (already carries the class, now actually visible)
  renders at the enlarged size whenever `text.scriptEnlarged` is true;
  Transcription and Translation stay at normal size regardless (they never
  carry the class).
- [ ] Dialog view: same, using `dialog.scriptEnlarged`.
- [ ] Vocabulary/Models table-cell enlargement is unaffected (still uses
  the plain `.prose-enlarged` rule, still uncontested there).
- [ ] No change to Edit-mode textarea enlargement (`editor.js`) — already
  correct, not touched.

## Approach

1. `phraseforge/internal/server/templates/layout.html`: add a compound
   rule immediately after the existing two (`article.prose.prose-enlarged
   { font-size: 1.4rem; line-height: 1.9; }`) so it outranks
   `article.prose`'s base rule specifically when both classes coexist on
   an article — the plain `.prose-enlarged` rule stays untouched for its
   table-cell use.
2. `phraseforge/internal/server/static/js/texts-app.js`: add
   `${text.scriptEnlarged ? " prose-enlarged" : ""}` to the Transcription
   (line ~524) and Translation (line ~529) `<article class="prose...">`
   elements, matching Source's existing conditional.
3. `phraseforge/internal/server/static/js/dialogs-app.js`: pass the same
   third argument to `renderedOrError(html, err, extraClass)` for
   Transcription (line ~476) and Translation (line ~481) that Source
   already passes (line ~472).

**Testing:** zero LLM calls. Verify live in the k3d cluster against the
Han-script text already in the DB (`scriptEnlarged: true`, confirmed via
`GET /api/v1/texts/10`) — inspect the *computed* font-size (not just the
class list, which was already "correct" and still silently failed) on all
three View-page tabs, and confirm Vocabulary/Models table cells are
unchanged.

## Affected Areas

- `phraseforge/internal/server/static/js/texts-app.js`
- `phraseforge/internal/server/static/js/dialogs-app.js`

## Out of Scope

- Any change to Edit-mode textarea enlargement.
- Any change to Vocabulary/Models (already correct).

## Implementation Notes

1. `phraseforge/internal/server/templates/layout.html`: added
   `article.prose.prose-enlarged { font-size: 1.4rem; line-height: 1.9; }`
   right after the two existing rules, with a comment explaining the
   specificity math so this doesn't get "simplified" away later. The
   plain `.prose-enlarged` rule is untouched.
2. **Reverted** (user correction after initial delivery): Transcription and
   Translation must stay normal size — only Source enlarges. Removed the
   `text.scriptEnlarged ? " prose-enlarged" : ""` conditional that had been
   added to `texts-app.js`'s Transcription/Translation `<article>` elements;
   they now render with a plain `class="prose"`, unconditionally.
3. **Reverted**, same reason: `dialogs-app.js`'s Transcription/Translation
   `renderedOrError(html, err)` calls no longer pass the third
   (`extraClass`) argument.
4. Self-reviewed the diff — clean, no findings; scoped exactly to the
   lines/rules described above (this feature's diff also carries forward
   some earlier-session uncommitted work in the same files, all already
   delivered and unrelated to this change).
5. Appended a `specs/memory.md` `[gotcha]` entry recording the CSS
   specificity pattern, since it's genuinely reusable for any future
   "modifier class added alongside a base class on the same element"
   case in this codebase.

## Validation

- `node --check` on both edited JS files, `go build`/`vet`/`test
  ./... -count=1` all pass — no regressions.
- **Rigorous automated proof, not just manual specificity math**: a
  jsdom script loaded the exact before/after CSS and asserted
  `getComputedStyle(...).fontSize` directly:
  - Before the fix: `article.prose.prose-enlarged` resolved to
    **18.4px (1.15rem)** — confirms the bug was real, not a false
    diagnosis.
  - After the fix: resolves to **22.4px (1.4rem)** — confirms the fix
    actually works, not just that the right CSS text is present.
  - The table-cell case (plain `.prose-enlarged`, no competing rule)
    still resolves to **22.4px** — confirms Vocabulary/Models are
    unaffected.
- **Post-correction**: the `prose-enlarged` class addition to
  Transcription/Translation was reverted (see Implementation Notes) — those
  two tabs now render with a plain `class="prose"` and are never affected
  by `scriptEnlarged`, on both Text and Dialog view. Only Source carries
  the class and is subject to the compound CSS rule above.
- Redeployed to the k3d cluster (`task deploy-phraseforge`); confirmed via
  `curl` that both served JS files now contain exactly 1
  `prose-enlarged`-conditional occurrence each (Source only), down from the
  earlier over-scoped 2 (`texts-app.js`) / 3 (`dialogs-app.js`).
- No browser available this session to visually confirm the rendered
  page — functional/computed-style behavior fully verified via the
  methods above; a visual spot-check is still worth doing next time
  you're in the app, on a Han/Arabic-script text with a transcription
  and translation.

## Documentation Review

Affected Areas map to the `phraseforge` artifact only — no fan-out.

**Changelog entry needed** (user-facing display bug, category `Fixed`):

- `{phraseforge/CHANGELOG.md, Fixed, "the Text/Dialog view page's enlarged-script display (for Han/Arabic/Hebrew/Syriac/Japanese/Korean scripts) now actually takes effect on the Source tab — a CSS specificity bug silently defeated it even though the class was already correctly applied; Transcription/Translation are unaffected and stay normal size, as intended."}`

**Other drift:** none found. No constitution file makes a claim this
change contradicts.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: added the `### Fixed` entry above.
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md` — it's in the changelog now.

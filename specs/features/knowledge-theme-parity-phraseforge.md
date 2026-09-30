---
title: Knowledge adopts phraseforge's palette, variable names, theme toggle, and header styling
kind: feature
status: done
version: 1
updated: 2026-09-30
branch: main
---

## Problem / Motivation

Knowledge and phraseforge look like different products. Knowledge's
`theme.css` has its own palette and names (`--fg`, `--hover-bg`,
`--tag-bg`, `--sources-bg`, `--accent`, `--accent-hover`, pure white/black
backgrounds); phraseforge's (`layout.html:28-57`) uses `--bg`, `--surface`,
`--surface-alt`, `--text`, `--link`, `--link-hover`, `--btn-bg`,
`--btn-bg-hover`, `--danger`, `--danger-hover`, `--success`, `--border`,
`--muted` with softer grey/blue tones. Knowledge's theme button is a static
🌓 on `index.html`, `login.html`, `signup.html`; phraseforge's shows ☽ in
light mode and ☀ in dark (`layout.html:356-358`). Phraseforge's header is a
3.5rem sticky bar on `--surface` with a bold brand and bordered 2.25rem
icon buttons; knowledge's is a plain flex row. The user noticed the theme
icon difference directly (2026-09-30).

This is knowledge's roadmap item `knowledge-theme-parity-phraseforge`:
"Adopt phraseforge's color palette, variable naming, dynamic sun/moon
theme-toggle icon, and menu/header styling in place of knowledge's current
theme, entirely via external CSS".

User decisions (2026-09-30):

- Restyle only: knowledge keeps its tab nav in the header and its per-tab
  asides; moving nav into a phraseforge-style sidebar stays with
  `knowledge-aside-collapse`.
- Rename knowledge's CSS variables to phraseforge's names everywhere
  (`theme.css`, `app.css`, `auth.css`, all kb-* `component.css`).

Assumptions (confirmed at approval, 2026-09-30):

- Body typography follows phraseforge too: `system-ui, -apple-system,
  "Segoe UI", sans-serif` at 17px (knowledge is `system-ui, sans-serif` at
  the browser's 16px), so text density matches across apps.
- The saved theme key stays `kb-theme`, so each user's current choice
  survives; the two apps keep separate choices, as today.
- Per `memory.md` 2026-09-24T11:12:14Z, this stays a shared *convention*
  (same names, values, and markup pattern in both apps), not shared files —
  matching the tech stack's deliberate-duplication rule for frontends.

## Acceptance Criteria

- `knowledge/internal/server/static/theme.css` defines exactly
  phraseforge's variables with phraseforge's light and dark values.
- No knowledge static file references a removed name (`--fg`,
  `--hover-bg`, `--tag-bg`, `--sources-bg`, `--accent`, `--accent-hover`),
  including `var(..., fallback)` fallbacks, whose fallback values become
  phraseforge's light values. Mapping: `--fg` → `--text`; `--hover-bg`,
  `--tag-bg`, `--sources-bg` → `--surface-alt`; `--accent-hover` →
  `--btn-bg-hover`; `--bg` → `--bg` for the page background and
  `--surface` for inputs, cards, dialogs, and the header; `--accent` →
  `--btn-bg` for filled buttons and the active nav item, `--link` for text
  links and focus rings; `--border`, `--muted`, `--danger`, `--success`
  unchanged.
- The theme toggle on all three pages shows ☽ in light mode and ☀ in dark
  mode, updates on click, and is correct on first paint.
- Header on `index.html`: phraseforge's topnav look (sticky, 3.5rem,
  `--surface` background, bottom border, 1.5rem padding and gaps, bold
  "Knowledge" brand); the icon button matches pf-button's icon variant
  (bordered 2.25rem square, hover border `--link`); the tab nav items use
  phraseforge's link/active colors.
- Body typography as in the Assumptions.
- All styling in external CSS files (no new inline `style=` attributes).
- `task test-knowledge-frontend` (kb-* component tests) passes; lab deploy
  renders all three pages correctly in light and dark (user visual check).

## Approach

1. **Palette and toggle**: replace `theme.css` with phraseforge's
   variables; extend `theme.js` with a `setIcon()` that writes ☽/☀ into a
   `#theme-icon` span (called on load and on toggle); change the three
   pages' toggle markup to `<span id="theme-icon"></span>`.
2. **Rename**: update the 63 variable uses across `app.css`, `auth.css`,
   and the 8 kb-* `component.css` files per the mapping, deciding `--bg`
   and `--accent` per use; grep proves no removed name remains.
3. **Header and typography**: `app.css` header rules and brand, kb-nav
   item colors, kb-button icon variant, body font; `auth.css` body font.
4. **Verify**: component tests; lab deploy (`task deploy-knowledge`); user
   visual check of index/login/signup in both themes.

## Affected Areas

- `knowledge/internal/server/static/theme.css`, `theme.js`, `app.css`,
  `auth.css`, `index.html`, `login.html`, `signup.html`
- `knowledge/internal/server/static/components/*/component.css` (kb-button,
  kb-card, kb-dialog, kb-field, kb-job-card, kb-message, kb-nav,
  kb-status-bar)

## Out of Scope

- Moving knowledge's nav into a sidebar, or collapsing asides
  (`knowledge-aside-collapse`).
- Sharing CSS files between the apps.
- Phraseforge changes.
- The Jobs page listing chat/generate operations (separate idea).

## Implementation Notes

1. `theme.css`: phraseforge's 13 variables with its exact light/dark values
   (copied from `phraseforge/.../templates/layout.html`). `theme.js`:
   `setIcon()` writes ☽ (light) / ☀ (dark) into `#theme-icon`, run on
   apply and on `DOMContentLoaded` (theme.js runs in `<head>`, so colors
   are right on first paint); key stays `kb-theme`. The three pages' 🌓
   toggle became `<span id="theme-icon"></span>` (kb-button moves its
   child nodes into its inner button, so the span survives) plus an
   `aria-label`.
2. Rename across `app.css`, `auth.css`, 8 kb-* `component.css`: per-use
   `--bg` → `--surface` for inputs/textareas and the dialog box (page
   bodies stay `--bg`); `--accent` → `--btn-bg` for the primary button,
   active nav item, active job badge, `--link` for the assistant message
   stripe; mechanical `--fg` → `--text`, `--hover-bg`/`--tag-bg`/
   `--sources-bg` → `--surface-alt`, `--accent-hover` → `--btn-bg-hover`.
   All `var(..., fallback)` fallbacks set to phraseforge's light values; a
   scan confirms no removed name remains and every referenced variable
   exists in the palette. `kb-button`: pf-button's colors/weights (base
   `--surface-alt`, hover `--border`, bold primary/danger, `--danger-hover`
   instead of a brightness filter) and its icon variant exactly (2.25rem
   bordered square, hover border `--link`); knowledge keeps its own
   padding/min-width.
3. Header (`app.css`): phraseforge's topnav (sticky, 3.5rem, `--surface`,
   bottom border, 1.5rem gap/padding), new `<a class="brand">Knowledge</a>`
   in `index.html`, header links muted with `--link` hover. `kb-nav` hover
   matches pf-tabs (`rgba(128,128,128,0.12)`). Body typography in `app.css`
   and `auth.css`: `system-ui, -apple-system, "Segoe UI", sans-serif`, 17px.
   Auth form card: `--surface` background, 0.6rem radius, 2rem padding
   (phraseforge's `.form-card`). No inline `style=` added.

## Validation

- `task test-knowledge-frontend`: 37 pass, 0 fail (baseline 37/37).
- Lab `task deploy-knowledge` rolled out; `/theme.css`, `/theme.js`,
  `/login.html` serve the new palette and `#theme-icon` markup.
- User visual check (2026-09-30): "Looks good".
- Follow-up requested at review and done: removed the header's Qdrant
  console link (and its now-unused `header a:not(.brand)` rules); renamed
  the first nav pill's label to "Documents" (`app.js`; tab id stays
  `knowledge`, so tab switching is unchanged). Frontend tests 37/37 after.
- Then (user request): the Documents tab's panel heading
  (`index.html` `<h1>`) renamed to "Documents"; tests 37/37; lab
  redeployed.

## Documentation Review

| Changelog | Category | Entry |
|---|---|---|
| `knowledge/CHANGELOG.md` | Changed | Phraseforge look (palette/names, ☽/☀ toggle, top bar, buttons, font, auth card); "Documents" tab; Qdrant link removed. |

Doc drift: `knowledge/README.md` said "The GUI also includes a link to the
Qdrant console" — no longer true. Constitution: no drift.

## Documentation Updates

- `knowledge/CHANGELOG.md` `[Unreleased] / Changed`: the entry above.
- `knowledge/README.md`: Qdrant console line now says it isn't linked from
  the GUI.
- `specs/artifacts/knowledge/roadmap.md`: `## Now` line removed.

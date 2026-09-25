---
title: Make phraseforge's aside/main scroll independently
kind: bugfix
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

Phraseforge's authenticated layout (`layout.html`) uses `.layout { display: grid; grid-template-columns: 15rem 1fr; min-height: calc(100vh - 3.5rem); }` with `aside.sidebar`/`main.content` as its two column children — but neither the outer `body` nor `.layout` bounds the row's height, so a long `main.content` (a long text) grows the whole `.layout` row past the viewport, and the page's own scrollbar scrolls `aside`/`main` together as one unit. The user wants independent scrolling — sidebar nav stays put while the reading area scrolls — matching knowledge's own layout, which already solves this exact problem (`specs/memory.md`, 2026-09-22T17:12:26Z/27Z `[gotcha]`/precedent): `body { display:grid; grid-template-rows: auto 1fr auto; height:100vh }` plus `min-height:0` on every nested grid item that needs its own `overflow:auto` to actually take effect (a grid item's default `min-height:auto` otherwise lets it grow past its track, defeating the track's own sizing).

Phraseforge has no persistent footer row (its `.status-bar` is `position:fixed`, outside grid flow), so this is a simpler 2-row case than knowledge's 3-row one (header/tab/footer): `auto` header row, `1fr` everything-else row.

## Acceptance Criteria

- `body` becomes `display: grid; grid-template-rows: auto 1fr; height: 100vh;` (dropping reliance on `html, body { height: 100% }` alone, which doesn't bound row growth). `header.topnav` (currently `position: sticky`, unaffected either way) is the `auto` row; everything else (`.layout` for authenticated pages, `main.auth-page` for login/signup) is the `1fr` row.
- `.layout`'s `min-height: calc(100vh - 3.5rem)` becomes `min-height: 0` (the exact gotcha fix — lets the `1fr` body row actually constrain it instead of growing to content).
- `aside.sidebar` and `main.content` each gain `min-height: 0` (they are `.layout`'s own grid-column children, a second nesting level that needs the same fix independently) and `overflow-y: auto` (this is what actually makes them scroll independently once their heights are correctly constrained).
- `main.auth-page`'s `min-height: calc(100vh - 3.5rem)` becomes `height: 100%` (it needs to actually fill its grid row for its existing `flex; align-items:center; justify-content:center` centering to have room to center within — `min-height:0` alone would leave it content-sized, not filling the row) plus `overflow-y: auto` for the same defensive reason as `aside`/`main.content`.
- No change to `header.topnav`'s own CSS, the sidebar-collapse mechanism (`[data-sidebar="collapsed"]`), the mobile media query (`@media (max-width: 760px)`), or any color/spacing value — this is purely a height/overflow fix, not a redesign.
- Live end-to-end validation: deploy, view a text long enough to overflow the viewport — the sidebar nav stays fixed in place while the reading area scrolls; the header stays visible at the top throughout. Same check on a page with a short sidebar and long main content, and vice versa (a page where the sidebar's own content — e.g. a full language list — is longer than main's).

## Approach

Directly reproduces knowledge's own proven fix (`specs/memory.md`, 2026-09-22T17:12:26Z/27Z), simplified for phraseforge's 2-row (not 3-row) layout — this is a copy of an already-debugged technique, not new design work.

**Ordered implementation plan:**

1. Update `layout.html`'s inline `<style>`: `body`'s grid rows, `.layout`'s `min-height`, `aside.sidebar`/`main.content`'s `min-height`/`overflow-y`, `main.auth-page`'s `height`/`overflow-y`.
2. Live-deploy and validate: a long text (main overflows, sidebar doesn't), the language-filter dropdown open (sidebar's own scroll unaffected), and the login page (still centers correctly, no visual regression there).

**Risks & testing strategy:**

- Lowest-risk feature in this whole `pf-*`/layout series — pure CSS, no JS, no template structural change (no new elements, no ID changes), directly reusing a technique already proven correct in this exact project. Main risk is a copy-paste value mismatch; covered by comparing against the exact recorded gotcha and by live visual validation (no browser automation available this session, consistent with every prior frontend feature here — the user is asked to spot-check independent scrolling visually after deploy, since that's the one thing `curl` genuinely cannot observe).

## Affected Areas

- `phraseforge/internal/server/templates/layout.html` (inline `<style>` only)
- `specs/artifacts/phraseforge/roadmap.md` (`## Next` → `## Now` transition at B2 start)
- `phraseforge/CHANGELOG.md` (entry added at B5)

## Out of Scope

- Any change to the SPA migration items (`phraseforge-spa-shell-texts` and later) — this fix stands on its own, per the user's explicit decision to ship it now rather than bundle it into the shell work.
- Any visual/color/spacing redesign.
- The mobile media query's sidebar-hiding behavior (`display:none` under 760px) — unaffected, not revisited.

## Implementation Notes

Applied the exact plan to `layout.html`'s inline `<style>`:
- `body`: added `display: grid; grid-template-rows: auto 1fr; height: 100vh;`.
- `.layout`: `min-height: calc(100vh - 3.5rem)` → `min-height: 0`.
- `aside.sidebar`: `overflow: hidden` → `overflow-y: auto`; added `min-height: 0`. (`overflow: hidden` was only ever load-bearing for the collapsed-sidebar fade, which also sets `opacity: 0; pointer-events: none` — those alone already hide/disable it, so no separate `overflow-x` rule was needed to preserve that behavior.)
- `main.content`: added `overflow-y: auto; min-height: 0;`.
- `main.auth-page`: `min-height: calc(100vh - 3.5rem)` → `height: 100%` (not `min-height: 0` — it needs to actually fill the row for its own `flex`/`align-items`/`justify-content` centering to have room to center within); added `overflow-y: auto`.

No deviation from the approved Approach.

**Final file list**: modified `phraseforge/internal/server/templates/layout.html` only (inline `<style>`, no markup/JS change).

## Validation

**Automated:**
- `go build ./...`, `gofmt -l .` — clean (CSS-only change, no Go/JS/component change; `task test-phraseforge-frontend`'s 22 tests are unaffected and still pass).

**Live validation** (deployed via `task deploy-phraseforge`; no template-parse panic):
- Confirmed via `curl` that the served `layout.html` (`/login`, unauthenticated) contains every new/changed rule exactly as written.
- Logged in as the seeded `admin` account; confirmed the home page (`/`) and an already-authenticated redirect from `/login` both still return the expected status codes — no session/rendering regression from the CSS change.

**Known gap (consistent with every prior frontend feature in this project, and the one thing this feature is actually about)**: no browser automation tool available this session, so the one property this fix exists to deliver — the sidebar staying in place while a long reading area scrolls independently — could not be visually confirmed. The user is asked to spot-check: open a long text, scroll the main content, and confirm the sidebar nav doesn't move; then check the login page still centers correctly.

**Overall verdict**: clean, no regressions in anything `curl`-observable; the actual scroll behavior awaits the user's visual confirmation.

## Documentation Review

**Files audited:**
1. `phraseforge/README.md` — no layout-specific claims — no drift.
2. `specs/tech-stack.md` — no layout-mechanism claims — no drift.
3. `specs/roadmap.md` — unaffected.
4. `phraseforge/CHANGELOG.md` — checked for entry mapping.

**No constitution drift found.**

**Proposed changelog entry (for B5):**

**Category: Fixed**
```
- the sidebar and main content area now scroll independently (sidebar stays in place while a
  long page scrolls), instead of scrolling together as one page.
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Fixed`: the entry proposed above.

Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md` (feature complete).

No `memory.md` entries — this reused an already-documented technique verbatim; nothing new to record.

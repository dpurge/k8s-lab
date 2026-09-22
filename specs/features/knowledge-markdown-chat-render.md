---
title: Render knowledge chat messages as sanitized Markdown HTML
kind: feature
status: done
version: 1
updated: 2026-09-22
branch: main
---

## Problem / Motivation

Today, `knowledge/internal/server/static/index.html`'s `renderMsg()` calls a hand-rolled `md()` function that HTML-escapes the entire message first, then does one regex pass to turn `[text](url)` into a link, then turns `\n` into `<br>` — nothing else (bold, headers, lists, tables, code) renders as anything but literal escaped text. This is the `knowledge-markdown-chat-render` roadmap item: render chat message markdown as real HTML using "a sanitizing/allowlist-based renderer (not raw innerHTML of arbitrary markdown)," preserving today's textContent-equivalent XSS safety.

Two things already produce markdown-worthy chat content today: `knowledge/internal/chat/chat.go`'s `appendReferences()` emits a literal `- [Title](url) — score X\n` bullet list for cited sources, and LLM-generated assistant answers commonly use bold/code/lists on their own.

The user decided (after being asked, since this app's frontend currently has zero external dependencies — a single vanilla-JS `index.html`, no npm, no CDN, no build step) to vendor two well-established, self-hosted (no CDN) libraries rather than hand-roll a wider allowlist parser or vendor nothing: **markdown-it** `15.0.2` for parsing (chosen for full CommonMark + GFM tables out of the box, verified live, plus a documented plugin architecture that leaves room for a custom syntax extension later, even though none is being built now) and **DOMPurify** `3.4.15` for sanitizing markdown-it's HTML output before it touches the DOM (chosen as a dedicated, industry-standard sanitizer rather than hand-written allowlist regex, satisfying the roadmap's explicit "sanitizing/allowlist-based renderer" requirement as a second, independent safety layer on top of markdown-it's own default safety).

Per the user's explicit decision, `<img>` tags are allowed (DOMPurify's default profile, no `FORBID_TAGS` override) — accepting that an image in rendered markdown auto-loads without a click, which for a message containing attacker-controlled markdown would function as a tracking pixel (leaks that the message was viewed, plus IP/user-agent, to whoever controls the image URL). This is a knowingly-accepted tradeoff, not an oversight.

## Acceptance Criteria

- [x] `knowledge/internal/server/static/vendor/markdown-it.min.js` (markdown-it 15.0.2's official `dist/browser/markdown-it.umd.min.js` build, global `markdownit`) and `knowledge/internal/server/static/vendor/purify.min.js` (DOMPurify 3.4.15's official `dist/purify.min.js` build, global `DOMPurify`) are vendored verbatim from their npm-published dist files — no CDN reference anywhere. A `knowledge/internal/server/static/vendor/LICENSES.md` records each library's name, version, license, and source URL.
- [x] No Go code changes are needed to serve the new vendor files: `knowledge/internal/server/server.go`'s existing `//go:embed static/*` and its catch-all `r.Handle("/*", http.FileServer(...))` (already behind the same `requireAuth` middleware as the rest of the app UI) already serve anything placed under `static/`.
- [x] `knowledge/internal/server/static/index.html`'s `<head>` gains two `<script src="vendor/markdown-it.min.js">`/`<script src="vendor/purify.min.js">` tags, before the existing inline `<script>` block.
- [x] The existing `md(value)` function is replaced with a version that runs `value` through a `markdownit({ linkify: true, breaks: true })` instance's `.render()`, then `DOMPurify.sanitize()` on the result, and returns that. `html` stays at markdown-it's default (`false`) — kept as explicit defense-in-depth even though DOMPurify also strips raw HTML. `renderMsg()`'s call site (`${md(message.content)}`) is unchanged.
- [x] Real markdown now renders as real HTML in chat messages: headers, bold/italic, inline and fenced code, unordered/ordered lists (including `chat.go`'s existing `- [Title](url) — score X` reference bullets, now a real `<ul>`), tables, links, and images.
- [x] A small amount of new CSS (scoped under the existing `.msg` rule) makes `pre`/`code`/`table`/`th`/`td`/`blockquote`/`ul`/`ol` legible — today none of these tags have any styling at all in this file.
- [x] XSS safety is preserved or improved, not weakened: a chat message containing a literal `<script>` tag, an `<img onerror=...>` attribute, or a `[link](javascript:...)` must not execute any script when rendered — verified live, not just by reasoning about the libraries' documented behavior.
- [x] No backend/Go change of any kind — this is entirely a static-frontend change.

## Approach

1. Download and vendor markdown-it 15.0.2's `dist/browser/markdown-it.umd.min.js` and DOMPurify 3.4.15's `dist/purify.min.js` (their official npm-published browser builds — fetched and verified live during research, not assumed) into `knowledge/internal/server/static/vendor/`, plus a `LICENSES.md` there.
2. Add the two `<script>` tags to `index.html`'s `<head>`.
3. Replace `md()` with the `markdownit()` + `DOMPurify.sanitize()` two-line version described above; delete the old regex-based implementation entirely.
4. Add the small scoped CSS block for previously-unstyled tags.
5. Live-validate: send real chat messages exercising every construct in the Acceptance Criteria, plus the three XSS probes, and confirm each renders (or is neutralized) correctly.

This is the app's first frontend dependency ever, chosen to be vendored rather than CDN-loaded specifically to avoid a new runtime dependency on external reachability (this app's philosophy elsewhere is local-first, no external accounts required). Allowing `<img>` is a deliberate, user-approved tradeoff (tracking-pixel/IP-leak risk), not mitigated further here. Relying on two independent, well-audited safety layers (markdown-it's own default-safe parsing plus DOMPurify's sanitization) rather than one hand-written allowlist reduces — doesn't eliminate — XSS risk from a parser or sanitizer bug; verified empirically for the specific probes above.

This app's frontend has no automated test infrastructure (pure static HTML/JS, no test framework, no prior precedent) — validation is live/manual against the Acceptance Criteria, which is a pre-existing gap, not one newly introduced by this feature.

## Affected Areas

- `knowledge/internal/server/static/index.html`
- `knowledge/internal/server/static/vendor/markdown-it.min.js` (new)
- `knowledge/internal/server/static/vendor/purify.min.js` (new)
- `knowledge/internal/server/static/vendor/LICENSES.md` (new)
- `specs/tech-stack.md` (constitution update — see documentation updates)

## Out of Scope

- Syntax highlighting inside fenced code blocks (would need an additional library, e.g., highlight.js — not requested).
- Building an actual custom markdown syntax extension now — per the user's decision, this feature only leaves the door open (markdown-it's plugin architecture), it doesn't build one.
- Forbidding `<img>` — per the user's explicit decision, images are allowed with the stated risk accepted.
- Any change to how `message.role` or `sources` are rendered — only `message.content`'s rendering path changes.
- Any backend/Go code change.

## Implementation Notes

Vendored markdown-it 15.0.2's official `dist/browser/markdown-it.umd.min.js` and DOMPurify 3.4.15's official `dist/purify.min.js` into `knowledge/internal/server/static/vendor/markdown-it.min.js` and `knowledge/internal/server/static/vendor/purify.min.js`. Downloaded from jsDelivr, byte-for-byte cross-verified against an independent second source (unpkg.com) before use. Created `knowledge/internal/server/static/vendor/LICENSES.md` recording each library's version, license, and source.

No Go code changes were needed: confirmed live that `knowledge/internal/server/server.go`'s existing `//go:embed static/*` plus its catch-all `r.Handle("/*", http.FileServer(...))` already serve any file placed under `static/`, including a new subdirectory — verified by curling `/vendor/markdown-it.min.js` and `/vendor/purify.min.js` against the deployed app and receiving `200` with expected byte sizes (115080 and 29369 bytes).

Modified `knowledge/internal/server/static/index.html`: added two `<script src="/vendor/...">` tags to `<head>` (absolute paths, matching existing convention for `/theme.js`). Replaced the old regex-based `md()` function (escape-then-single-link-regex-then-`<br>`) with `DOMPurify.sanitize(markdownRenderer.render(value || ""))`, where `markdownRenderer = markdownit({ linkify: true, breaks: true })` is a module-scope const. Left `html` at markdown-it's default (`false`). Added a scoped CSS block under `.msg` styling `pre`/`code`/`table`/`th`/`td`/`blockquote`/`ul`/`ol`, since none had prior styling.

## Validation

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...` in `knowledge/` all passed; this feature touched no Go code.
- Live-deployed via `task deploy-knowledge`; both vendor files served with `200` and correct byte sizes through the running app.
- Validated the markdown rendering pipeline by executing the real vendored files via Node.js with `jsdom`-backed `window`/`document` (required by DOMPurify). Ran the exact `markdownit({linkify:true,breaks:true})` + `DOMPurify.sanitize()` call sequence used in `index.html` for real, not reasoned about. Tested inputs: headers, bold/italic, inline code, fenced code blocks, unordered lists (using `chat.go`'s actual `- [Title](url) — score X` format, confirming it renders as real `<ul><li>`), ordered lists, GFM tables, links, and images.
- XSS safety verified: all three required probes neutralized: raw `<script>alert(1)</script>` renders as escaped literal text (not executed), `<img src=x onerror="alert(1)">` likewise renders as escaped text, `[click me](javascript:alert(1))` rejected by markdown-it's link validator and falls back to literal text. Malformed-URL injection attempt (`![x](x" onerror="alert(1))`) also failed to parse and rendered as literal text.
- Confirmed via live deployed API: sent a chat message containing `**bold test** <script>alert(1)</script> and [a link](javascript:alert(1))` as literal user input, verified via `GET /api/v1/chats/{id}` that the backend stores and returns this verbatim, unmodified — confirming markdown rendering/sanitization is purely frontend, not a backend concern.
- All test data created during validation was deleted afterward.
- Known gap (pre-existing): this app's frontend has no automated test framework — validation was live/manual for rendering, or real-code execution under Node+jsdom for sanitization (no browser-automation tool was available).

## Documentation Review

Checked `knowledge/README.md`'s "Chat with knowledge" section against this change: found it had no mention of markdown rendering (only noted "chat messages" and "sources" in passing). `specs/tech-stack.md` (already updated to constitution v2) adds markdown-it and DOMPurify to the tech stack — that update was gated as its own change before implementation started (per the spec's Approach section). No other documentation drift found.

## Documentation Updates

`knowledge/README.md`'s "Chat with knowledge" section gained one clause noting message content now renders as sanitized Markdown (already done as part of the prior drift fix). `CHANGELOG.md`'s `[Unreleased]` section gains a new bullet under `### Added` (see Changelog update, this session). `specs/tech-stack.md` constitution update (v1→v2, adding markdown-it/DOMPurify) was already handled as a gated change before implementation — no duplication here.

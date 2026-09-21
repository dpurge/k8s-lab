---
version: 7
status: approved
updated: 2026-09-21
---

## Now

## Next

- `knowledge-prompts-configmap` — Move model prompt templates (chat/generate/translate system prompts) from Go source into the mounted ConfigMap YAML, so prompts are editable without a rebuild.
- `knowledge-markdown-chat-render` — Render chat message markdown as HTML instead of plain text, using a sanitizing/allowlist-based renderer (not raw innerHTML of arbitrary markdown) to preserve the existing textContent-equivalent XSS safety.

## Later

## Out of scope

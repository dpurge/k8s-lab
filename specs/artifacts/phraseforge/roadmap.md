---
version: 32
status: approved
updated: 2026-10-20
---

## Now

## Next

## Later
- `phraseforge-grammar-from-knowledge` — Generate Grammar for a text sourced from knowledge's data, reusing the job-kind-registration pattern established by `phraseforge-job-queue`; cross-app data access (how phraseforge reads knowledge's content) is not yet decided
- `snake-case-migration` — Migrate the remaining camelCase configuration keys and admin API fields (config.yaml purposes and ingest keys, the Helm chart values, the older admin JSON such as llmPrompts and sourceLanguage) to snake_case per the naming conventions in tech-stack.md, rejecting each old name with a message naming its replacement; knowledge and dictionary have not been audited yet

## Out of scope

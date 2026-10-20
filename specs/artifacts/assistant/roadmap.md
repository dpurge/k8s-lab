---
version: 7
status: approved
updated: 2026-10-20
---

## Now

## Next

- `assistant-response-review-loop` — structured response documents rendered from templates, with a reviewer model and a bounded fix loop before sending
- `assistant-telegram` — Telegram as a task source and response channel, user tracking from Telegram identity, a Telegram-shaped mock endpoint and then the real channel (the mock chat page, several conversations per user and `/help` exist), a `/learn` command
- `assistant-memory` — long-term memory on pgvector with bge-m3 embeddings through trpc-agent-go
- `assistant-rag` — markdown knowledge documents with tags, import and export, search as a tool, plus web search
- `assistant-nats` — tasks in and results out over NATS
- `assistant-scheduler` — in-app scheduled tasks for outside-world awareness and user chores
- `assistant-auth` — authentication through a shared external microservice
- `assistant-prod-deploy` — Helm chart in the chart repository, Prometheus scrape job and deployment to the production cluster

## Later

## Out of scope
